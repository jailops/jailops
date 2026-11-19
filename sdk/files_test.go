package sdk_test

import (
	"archive/tar"
	"bytes"
	"io"
	"strings"
	"testing"

	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

func TestJailFilesAgainstFake(t *testing.T) {
	srv, c := newEnv(t)
	seedBasics(srv)
	mustJail(t, c, "web", webSpec())
	ctx := ctxT(t)
	srv.SeedJailFile("web", "web", "/etc/motd", []byte("welcome\n"), 0o644)
	j := c.Jails()

	st, err := j.FileStat(ctx, "web", "/etc/motd")
	if err != nil || st.Type != api.FileTypeFile || st.Size != 8 || st.Mode != "0644" {
		t.Fatalf("%v %+v", err, st)
	}
	if st, err = j.FileStat(ctx, "web", "/etc/none"); err != nil || st.Type != api.FileTypeNone {
		t.Fatalf("%v %+v", err, st)
	}
	if _, err = j.FileStat(ctx, "ghost", "/etc/motd"); !api.IsCode(err, api.CodeNotFound) {
		t.Fatalf("ghost: %v", err)
	}
	if _, err = j.FileStat(ctx, "web", "/etc/../x"); !api.IsCode(err, api.CodeInvalid) {
		t.Fatalf("dot dot: %v", err)
	}

	d, err := j.FileGet(ctx, "web", "/etc/motd", sdk.FileGetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(d)
	d.Close()
	if err != nil || string(b) != "welcome\n" || d.Size != 8 || d.Mode != "0644" {
		t.Fatalf("%q %v size=%d mode=%s", b, err, d.Size, d.Mode)
	}
	if _, err = j.FileGet(ctx, "web", "/etc/missing", sdk.FileGetOptions{}); !api.IsCode(err, api.CodeNotFound) {
		t.Fatalf("missing: %v", err)
	}

	res, err := j.FilePut(ctx, "web", "/etc/new", strings.NewReader("data"), sdk.FilePutOptions{Length: 4, Mode: 0o600})
	if err != nil || res.Files != 1 || res.Bytes != 4 {
		t.Fatalf("%v %+v", err, res)
	}
	if got, ok := srv.JailFile("web", "web", "/etc/new"); !ok || string(got) != "data" {
		t.Fatalf("%q %v", got, ok)
	}

	var tb bytes.Buffer
	tw := tar.NewWriter(&tb)
	tw.WriteHeader(&tar.Header{Name: "t.txt", Mode: 0o644, Size: 3, Typeflag: tar.TypeReg})
	tw.Write([]byte("tar"))
	tw.Close()
	if res, err = j.FilePut(ctx, "web", "/etc", &tb, sdk.FilePutOptions{Archive: true}); err != nil || res.Files != 1 {
		t.Fatalf("%v %+v", err, res)
	}
	d, err = j.FileGet(ctx, "web", "/etc/t.txt", sdk.FileGetOptions{Archive: true})
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(d)
	h, err := tr.Next()
	if err != nil || h.Name != "t.txt" {
		t.Fatalf("%+v %v", h, err)
	}
	if b, _ := io.ReadAll(tr); string(b) != "tar" {
		t.Fatalf("%q", b)
	}
	d.Close()
}
