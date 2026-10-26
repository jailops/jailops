package format

import (
	"bytes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"runtime"
	"sync"

	"github.com/klauspost/compress/zstd"
	"golang.org/x/crypto/chacha20poly1305"
)

const (
	blobMagic    = "JOCH"
	blobVersion  = 1
	blobHeader   = 4 + 1 + 1 + 2
	flagZstd     = 1
	trailerSize  = sha256.Size
	blobOverhead = blobHeader + NonceSize + chacha20poly1305.Overhead + trailerSize
)

// MaxChunkSize bounds the decompressed size accepted when decoding a chunk
// (zip-bomb guard). The chunker never produces more than 8 MiB; a pack of
// small files stays under 3 MiB.
const MaxChunkSize = 16 << 20

// MaxBlobSize is the largest chunk blob a repository accepts.
const MaxBlobSize = MaxChunkSize + blobOverhead

// zstd encoders and decoders. A klauspost Encoder or Decoder built with
// concurrency 1 serialises every call on it, so one shared instance would make
// a worker pool compress one chunk at a time. Every call takes its own
// instance from a free list; all are built with the same options, so the bytes
// are identical whichever instance produced them (pinned by a golden test).
func newZEnc() *zstd.Encoder {
	e, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault), zstd.WithEncoderConcurrency(1))
	return e
}

func newZDec() *zstd.Decoder {
	d, _ := zstd.NewReader(nil, zstd.WithDecoderMaxMemory(MaxChunkSize), zstd.WithDecoderConcurrency(1))
	return d
}

var zFree = runtime.GOMAXPROCS(0) * 2

var (
	encFree = make(chan *zstd.Encoder, zFree)
	decFree = make(chan *zstd.Decoder, zFree)
	scratch = sync.Pool{New: func() any { return new([]byte) }}
)

func getZEnc() *zstd.Encoder {
	select {
	case e := <-encFree:
		return e
	default:
		return newZEnc()
	}
}

func putZEnc(e *zstd.Encoder) {
	select {
	case encFree <- e:
	default:
	}
}

func getZDec() *zstd.Decoder {
	select {
	case d := <-decFree:
		return d
	default:
		return newZDec()
	}
}

func putZDec(d *zstd.Decoder) {
	select {
	case decFree <- d:
	default:
	}
}

// compress returns the zstd frame of src in a new slice.
func compress(src []byte) []byte {
	e := getZEnc()
	defer putZEnc(e)
	return e.EncodeAll(src, nil)
}

// EncodeOpts tune blob encoding.
type EncodeOpts struct {
	// NoCompress disables zstd.
	NoCompress bool
	// Nonce overrides the random nonce (tests only).
	Nonce []byte
}

// blobAAD binds the header, the key domain and the chunk id to the ciphertext.
func (k *Keys) blobAAD(flags byte, id ID) []byte {
	aad := make([]byte, 0, blobHeader+DomainSize+IDSize)
	aad = append(aad, blobMagic...)
	aad = append(aad, blobVersion, flags, 0, 0)
	aad = append(aad, k.domain[:]...)
	aad = append(aad, id[:]...)
	return aad
}

// EncodeChunk returns the keyed chunk id and the encrypted blob.
func (k *Keys) EncodeChunk(plaintext []byte, o EncodeOpts) (ID, []byte, error) {
	id := k.ChunkID(plaintext)
	blob, err := k.SealChunk(id, plaintext, o)
	return id, blob, err
}

// SealChunk compresses (unless that does not help) and encrypts plaintext under
// the already computed id. Callers compute the id first so that a chunk the
// repository already holds is never compressed or encrypted.
func (k *Keys) SealChunk(id ID, plaintext []byte, o EncodeOpts) ([]byte, error) {
	if len(plaintext) > MaxChunkSize {
		return nil, fmt.Errorf("chunk of %d bytes exceeds the maximum of %d", len(plaintext), MaxChunkSize)
	}
	body := plaintext
	var flags byte
	if !o.NoCompress {
		e := getZEnc()
		bp := scratch.Get().(*[]byte)
		if need := e.MaxEncodedSize(len(plaintext)); cap(*bp) < need {
			*bp = make([]byte, 0, need)
		}
		c := e.EncodeAll(plaintext, (*bp)[:0])
		putZEnc(e)
		defer func() { *bp = c[:0]; scratch.Put(bp) }()
		if len(c) < len(plaintext) {
			body, flags = c, flagZstd
		}
	}
	nonce := o.Nonce
	if nonce == nil {
		nonce = make([]byte, NonceSize)
		if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
			return nil, err
		}
	} else if len(nonce) != NonceSize {
		return nil, errors.New("bad nonce size")
	}
	aead, err := chacha20poly1305.NewX(k.Enc[:])
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, blobHeader+NonceSize+len(body)+chacha20poly1305.Overhead+trailerSize)
	out = append(out, blobMagic...)
	out = append(out, blobVersion, flags, 0, 0)
	out = append(out, nonce...)
	out = aead.Seal(out, nonce, body, k.blobAAD(flags, id))
	sum := sha256.Sum256(out)
	return append(out, sum[:]...), nil
}

// VerifyBlob checks structure and the SHA-256 trailer. It needs no key, so a
// keyless node can use it.
func VerifyBlob(blob []byte) error {
	if len(blob) < blobOverhead {
		return errors.New("blob too short")
	}
	if len(blob) > MaxBlobSize {
		return errors.New("blob too large")
	}
	if string(blob[:4]) != blobMagic {
		return errors.New("bad blob magic")
	}
	if blob[4] != blobVersion {
		return fmt.Errorf("unsupported blob version %d", blob[4])
	}
	body, tr := blob[:len(blob)-trailerSize], blob[len(blob)-trailerSize:]
	sum := sha256.Sum256(body)
	if subtle.ConstantTimeCompare(sum[:], tr) != 1 {
		return errors.New("blob trailer digest mismatch")
	}
	return nil
}

// DecodeChunk verifies, decrypts and decompresses blob. id is the id the blob
// is stored under; it is part of the associated data, and the keyed id of the
// decoded plaintext is recomputed and compared with it, which detects a
// self-consistent blob planted under a wrong id.
func (k *Keys) DecodeChunk(blob []byte, id ID) ([]byte, error) {
	if err := VerifyBlob(blob); err != nil {
		return nil, err
	}
	if id.IsZero() {
		return nil, errors.New("chunk id required to decode")
	}
	flags := blob[5]
	if flags&^byte(flagZstd) != 0 || blob[6] != 0 || blob[7] != 0 {
		return nil, errors.New("unsupported blob flags")
	}
	nonce := blob[blobHeader : blobHeader+NonceSize]
	ct := blob[blobHeader+NonceSize : len(blob)-trailerSize]
	aead, err := chacha20poly1305.NewX(k.Enc[:])
	if err != nil {
		return nil, err
	}
	var dst []byte
	if flags&flagZstd != 0 {
		bp := scratch.Get().(*[]byte)
		defer func() { *bp = dst[:0]; scratch.Put(bp) }()
		dst = (*bp)[:0]
	}
	pt, err := aead.Open(dst, nonce, ct, k.blobAAD(flags, id))
	if err != nil {
		return nil, errors.New("chunk authentication failed (wrong key, wrong id or corruption)")
	}
	if flags&flagZstd != 0 {
		dst = pt
		d := getZDec()
		pt, err = d.DecodeAll(pt, nil)
		putZDec(d)
		if err != nil {
			return nil, fmt.Errorf("decompressing chunk: %w", err)
		}
	}
	if len(pt) > MaxChunkSize {
		return nil, errors.New("chunk exceeds the maximum size")
	}
	if got := k.ChunkID(pt); got != id {
		return nil, errors.New("chunk id does not match its content")
	}
	return pt, nil
}

// ---- helpers shared with index, manifest and catalog ------------------------

func putU64(b []byte, v uint64) []byte { return binary.BigEndian.AppendUint64(b, v) }

func appendTrailer(b []byte) []byte {
	sum := sha256.Sum256(b)
	return append(b, sum[:]...)
}

func checkTrailer(b []byte) ([]byte, error) {
	if len(b) < trailerSize {
		return nil, errors.New("too short")
	}
	body, tr := b[:len(b)-trailerSize], b[len(b)-trailerSize:]
	sum := sha256.Sum256(body)
	if !bytes.Equal(sum[:], tr) {
		return nil, errors.New("trailer digest mismatch")
	}
	return body, nil
}

func newAEAD(key []byte) (cipher.AEAD, error) { return chacha20poly1305.NewX(key) }
