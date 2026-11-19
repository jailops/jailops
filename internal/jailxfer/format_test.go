package jailxfer

import (
	"bytes"
	"errors"
	"io"
	"math/rand/v2"
	"testing"

	"jailops.net/jailops/sdk/api"
)

func sample(t *testing.T, compress bool) ([]byte, [][]byte) {
	t.Helper()
	var buf bytes.Buffer
	m := api.ExportManifest{Name: "web", Project: "shop", UUID: "u1", Layout: api.LayoutThin}
	w, err := NewWriter(&buf, m)
	if err != nil {
		t.Fatal(err)
	}
	var datas [][]byte
	r := rand.New(rand.NewPCG(1, 2))
	for i, n := range []int{0, 10, chunk + 7, 3*chunk + 1} {
		d := make([]byte, n)
		for j := range d {
			d[j] = byte(r.IntN(7))
		}
		datas = append(datas, d)
		p := api.ExportPart{Dataset: []string{"root", "etc", "local", "home"}[i], Kind: api.PartPersistent, Snapshot: "export"}
		if compress {
			p.Compression = "zstd"
		}
		pw, err := w.Part(p)
		if err != nil {
			t.Fatal(err)
		}
		// write in odd pieces, like zfs send does
		for off := 0; off < len(d); {
			k := min(len(d)-off, 1+r.IntN(5000))
			if _, err := pw.Write(d[off : off+k]); err != nil {
				t.Fatal(err)
			}
			off += k
		}
		if err := pw.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), datas
}

func readAll(b []byte) (api.ExportManifest, [][]byte, error) {
	r, err := NewReader(bytes.NewReader(b))
	if err != nil {
		return api.ExportManifest{}, nil, err
	}
	var out [][]byte
	for {
		_, pr, err := r.Next()
		if errors.Is(err, io.EOF) {
			return r.Manifest(), out, nil
		}
		if err != nil {
			return r.Manifest(), out, err
		}
		d, err := io.ReadAll(pr)
		if err != nil {
			return r.Manifest(), out, err
		}
		out = append(out, d)
	}
}

func TestRoundTrip(t *testing.T) {
	for _, compress := range []bool{false, true} {
		b, want := sample(t, compress)
		m, got, err := readAll(b)
		if err != nil {
			t.Fatalf("compress=%v: %v", compress, err)
		}
		if m.Name != "web" || m.Format != api.ExportFormat || len(got) != len(want) {
			t.Fatalf("manifest %+v parts %d", m, len(got))
		}
		for i := range want {
			if !bytes.Equal(got[i], want[i]) {
				t.Fatalf("compress=%v: part %d differs", compress, i)
			}
		}
	}
}

func TestEveryTruncationIsDetected(t *testing.T) {
	b, _ := sample(t, false)
	// cut the file at many places; none may look complete
	for _, n := range []int{0, 5, len(Magic), len(Magic) + 5, len(Magic) + 100, len(b) / 3, len(b) / 2, len(b) - 200, len(b) - 20, len(b) - 1} {
		if _, _, err := readAll(b[:n]); err == nil {
			t.Fatalf("a file cut at %d of %d bytes was accepted", n, len(b))
		}
	}
}

func TestACorruptByteIsDetected(t *testing.T) {
	b, _ := sample(t, false)
	for _, off := range []int{len(b) / 4, len(b) / 2, len(b) - 60} {
		c := bytes.Clone(b)
		c[off] ^= 0xff
		if _, _, err := readAll(c); err == nil {
			t.Fatalf("a flipped byte at %d was accepted", off)
		}
	}
}

func TestNotAnExportFile(t *testing.T) {
	if _, err := NewReader(bytes.NewReader([]byte("hello world, this is not it\n"))); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("got %v", err)
	}
	if _, err := NewReader(bytes.NewReader(nil)); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("got %v", err)
	}
}

func TestSkippingAPartStillVerifiesIt(t *testing.T) {
	b, _ := sample(t, false)
	c := bytes.Clone(b)
	// flip a byte in the second part's data (after the first part); Next must report it when it skips the part
	c[len(c)-300] ^= 0x01
	r, err := NewReader(bytes.NewReader(c))
	if err != nil {
		t.Fatal(err)
	}
	var last error
	for {
		_, _, err := r.Next()
		if err != nil {
			last = err
			break
		}
	}
	if errors.Is(last, io.EOF) {
		t.Fatal("skipped corruption was not reported")
	}
}
