// Package format implements the byte-level formats of the backup repository:
// key derivation, chunk blobs, indexes, manifests, commit receipts and the
// framing of catalogs. This comment is the specification. Every construction
// has deterministic test vectors in testdata/vectors.json, re-checked by an
// independent implementation in scripts/verify-vectors.py that does not use
// this code.
//
// This is format version 1. The vectors are never regenerated to make a test
// pass: a change of any byte layout, key derivation, associated data, chunking
// parameter or gear table is a format change, and a format change bumps the
// version of the object concerned.
//
// # Primitives
//
// HKDF-SHA256, HMAC-SHA256, SHA-256, XChaCha20-Poly1305 (24-byte random nonce),
// Argon2id (key file only), X25519 through age (escrow only), zstd. All integers
// in the formats below are big endian unless stated. Varints are the Go
// encoding/binary uvarint and varint (zigzag) encodings.
//
// # Keys
//
// One 32-byte master key per repository. Sub-keys are derived with HKDF-SHA256,
// salt "JailOps/backup/v1", fixed info strings, 32 bytes of output:
//
//	k_enc       info "chunk-encryption"      chunk blob encryption
//	k_id        info "chunk-id"              keyed chunk identity, group ids
//	k_manifest  info "manifest-encryption"   manifest encryption
//	domain      info "domain-id"             first 8 bytes, 16 lowercase hex characters
//
// The domain id is not secret. A repository records it so that a host holding a
// different master key is refused. Rotating the master key is not possible (ids
// depend on it): a new key means a new repository.
//
// Recovery key: "JOR1-" followed by the base32 (RFC 4648, no padding) of
// master || SHA-256("JailOps backup recovery key v1" || master)[:4], in groups
// of five characters separated by "-". Parsing ignores case, spaces and dashes
// and verifies the checksum.
//
// # Chunk id
//
//	id = HMAC-SHA256(k_id, plaintext)            32 bytes, lowercase hex name
//
// computed over the plaintext, before compression and encryption. It is keyed, so
// the server cannot test whether it holds known data, and equal data under another
// key has another id. The zero id (32 zero bytes) means "all zero bytes, no blob"
// and appears only in indexes.
//
// # Chunk blob
//
//	magic     4   "JOCH"
//	version   1   0x01
//	flags     1   bit 0: the plaintext was zstd compressed before encryption
//	reserved  2   0x0000
//	nonce    24   random
//	ct        n   XChaCha20-Poly1305(k_enc, nonce, [zstd](plaintext), aad)
//	trailer  32   SHA-256 over every preceding byte
//
//	aad = magic | version | flags | reserved | domain (8 raw bytes) | chunk id (32)
//
// The trailer is unkeyed: a keyless node verifies it and detects bit rot,
// truncation and torn writes. The associated data binds the blob to its id and
// key domain, so a valid blob cannot be moved to another name or another
// repository. The node cannot see whether a blob's content matches its id: a
// holder of the key can plant a self-consistent blob under a wrong id (chunk id
// poisoning). Deep verification, run by a host that holds the key, decrypts each
// blob and recomputes the id.
//
// Compression is zstd at the library's default level, one encoder per goroutine,
// skipped when the result is not smaller (flag bit 0 says which form was stored).
// The output bytes are pinned by a vector: changing encoder options is a format
// change. A decoded chunk larger than 16 MiB (MaxChunkSize) is refused.
//
// # Chunking
//
// Files of 256 KiB or more are cut with FastCDC with normalised chunking:
// minimum 256 KiB, average 2 MiB, maximum 8 MiB. The 64-bit gear table is the 256
// outputs of splitmix64 started from the seed 0x6a61696c2e62652f (the ASCII bytes
// "JailOps/"), stepping x += 0x9e3779b97f4a7c15 and mixing
// z = (z ^ z>>30) * 0xbf58476d1ce4e5b9; z = (z ^ z>>27) * 0x94d049bb133111eb;
// out = z ^ z>>31. The hash is h = (h<<1) + gear[byte], warmed over the 64 bytes
// before the minimum size. The mask of the first phase (up to the average size) is
// the top bits+2 bits, of the second phase the top bits-2 bits, where bits is
// log2 of the average. A boundary is the first position where h & mask == 0. The
// gear table is part of the format: changing it destroys deduplication against
// existing backups. chunker/testdata/vectors.json pins the table and the cut
// lengths of a deterministic corpus.
//
// Files smaller than 256 KiB are packed in walk order. A pack is one chunk. A pack
// ends when it reaches 2 MiB, or, once it holds at least 512 KiB, after a file whose
// path has FNV-1a-32(path) mod 8 == 0. Pack boundaries therefore depend only on
// names and sizes, so adding or removing a file disturbs only the packs up to the
// next name-defined boundary. The catalog is cut separately, starting on a chunk
// boundary, with the same FastCDC parameters; payload and catalog never share a
// chunk. All-zero chunks get the zero id and no blob.
//
// # Stream layout
//
// A dataset stream is the bytes of every regular file with content, concatenated
// in catalog pre-order (the payload), followed by the encoded catalog. Both ranges
// start and end on chunk boundaries. A snapshot holds the streams of all datasets
// of a jail, back to back, in one index; the manifest records where each begins.
//
// # Index
//
//	magic       4  "JOIX"
//	version     1  0x01
//	kind        1  0x02 (dynamic chunks; 0x01 is reserved for fixed-size block chunks)
//	reserved    2  0
//	total_size  8  logical size in bytes
//	count       8  number of entries
//	entries     count * (end u64 | id 32 bytes)
//	trailer    32  SHA-256 over every preceding byte
//
// Entry i covers [end(i-1), end(i)). Ends strictly increase, the last equals
// total_size, no entry exceeds 16 MiB. The index is plaintext on purpose: the
// keyless node runs deduplication, garbage collection and verification from it.
// The node sees the chunk length sequence; that is documented.
//
// # Manifest
//
//	magic "JOMF" | version 1 | reserved 3 | nonce 24 |
//	XChaCha20-Poly1305(k_manifest, nonce, JSON, aad) | trailer SHA-256 (32)
//
//	aad = magic | version | reserved | group id | 0x00 | snapshot id
//
// The associated data binds the manifest to its location. The JSON (see
// Manifest) names the snapshot, the jail, the datasets (streams) with their
// offsets in the index, payload and catalog sizes, ZFS snapshot identity,
// full or incremental mode and last full read time, the restore bundle, and
// statistics, and carries the SHA-256 of the index file.
//
// # Group and snapshot ids
//
//	group    = hex(HMAC-SHA256(k_id, "group" 0x00 host 0x00 jail)[:16])   32 hex characters
//	snapshot = YYYYMMDDHHMMSS (UTC) "-" 8 hex characters
//
// host and jail are identities (uuids), so renaming a jail keeps its group. The
// repository tree carries no names. Retention uses the node's commit time from the
// receipt, never the id.
//
// # Commit receipt
//
// commit.json is a JSON object {format, committed, chunk_count, index_bytes,
// index_digest, manifest_digest, source}, written last and create-only. A snapshot
// exists if and only if it exists. A retried commit of identical bytes succeeds.
//
// # Catalog
//
//	header   8   "JOCT" | version 1 | reserved 3
//	body         one root directory entry
//	trailer 32   SHA-256 over every preceding byte
//
// An entry is:
//
//	kind        1    1 dir, 2 file, 3 symlink, 4 hardlink, 5 char, 6 block, 7 fifo
//	name        str  uvarint length + bytes; one path component; "" only for the root
//	mode        uvarint  permission bits and setuid, setgid, sticky (07777)
//	uid, gid    uvarint
//	mtime_ns    varint
//	ctime_ns    varint
//	inode       uvarint
//	flags       uvarint  file flags (chflags(2)); 0 where the platform has none
//	attrs       uvarint count, then count * (name str | value uvarint length + bytes),
//	            sorted strictly by name; opaque named blobs for extended attributes
//	            and ACLs; the name says what the blob is: "linux:xattr:<name>",
//	            "freebsd:extattr:user:<name>", "freebsd:extattr:system:<name>",
//	            "freebsd:acl:nfs4"
//	body        by kind:
//	  dir       children in strictly increasing name order, each a full entry,
//	            then a single 0x00 byte
//	  file      size uvarint; if size > 0: payload offset uvarint, SHA-256 of the content (32)
//	  symlink   target str
//	  hardlink  the path, relative to the root, of the first link, str
//	  char/block  major uvarint, minor uvarint
//	  fifo      nothing
//
// A decoder rejects a bad digest or header, unknown kinds, unsafe names (containing
// "/" or NUL, ".", ".."), unsorted or repeated names, nesting deeper than 4096,
// oversized attributes and trailing bytes. Sockets are not archived. The catalog
// drives change detection at the next backup, browsing, single file extraction by
// payload offset, and the per-file SHA-256 check at extraction. It is chunked and
// encrypted like data, so the node learns no file name.
//
// # Key file
//
// JSON, version 1. Protection "none" holds the master key in base64 and relies on
// file mode 0600. Protection "passphrase" holds XChaCha20-Poly1305 of the master key
// under a key from Argon2id (default time 3, memory 64 MiB, 4 lanes, 16-byte salt)
// with associated data "JailOps backup keyfile v1|" followed by the domain id.
// Stored Argon2 parameters are range-checked before use.
//
// # Escrow
//
// The master key wrapped to offline recovery recipients with age (X25519): an age
// file whose plaintext is "JOEK" | 0x01 | domain id (16 ascii) | master key (32).
// Stored at escrow/<domain>.age, create-only. The blob is not secret.
//
// # Repository keys
//
// See package store for the key layout and the commit protocol: chunks/aa/bb/<id>,
// groups/<group>/<snapshot>/{index,manifest,commit.json,protected,notes,verify.json},
// quarantine/<id>, escrow/<domain>.age, datastore.json and owner.json.
package format
