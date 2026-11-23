package service_test

import (
	"archive/tar"
	"bytes"
	"io"
	"strings"
	"testing"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/internal/service"
	"jailops.net/jailops/sdk/api"
)

// A copy into or out of a jail's root is kept apart from the operations on the jail: while one has the jail (an
// in-place upgrade checks the root's links before pkg runs on it as root of the host) or a package operation holds it,
// the copy is refused at once with busy; afterwards it goes through.
func TestJailFilesWaitForNoOperation(t *testing.T) {
	e := dtest.New(t, dtest.Options{})
	e.Seed()
	s := e.D.Service()
	createJail(t, e, s, "web")
	ctx := as(globalAdmin, dtest.SeedProject)
	put := func() error {
		_, _, err := s.JailFilePut(ctx, "web", "/etc", service.JailFilePutOpts{}, tarOf(t, "x", "y"))
		return err
	}
	unlock, err := service.LockJailForTest(s, "alpha_web")
	if err != nil {
		t.Fatal(err)
	}
	if err := put(); !api.IsCode(err, api.CodeBusy) {
		t.Fatalf("a copy while an operation has the jail: %v", err)
	}
	if _, _, err := s.JailFileGet(ctx, "web", "/etc/hosts", service.JailFileGetOpts{}, &strings.Builder{}); !api.IsCode(err, api.CodeBusy) {
		t.Fatalf("a read while an operation has the jail: %v", err)
	}
	unlock()
	release, err := service.HoldForTest(s, "alpha_web", "an in-place upgrade")
	if err != nil {
		t.Fatal(err)
	}
	if err := put(); !api.IsCode(err, api.CodeBusy) || !strings.Contains(err.Error(), "in-place upgrade") {
		t.Fatalf("a copy while a package operation holds the jail: %v", err)
	}
	release()
	// the fake host has no mounted root: what answers now is the copy itself, not the guard
	if err := put(); err != nil && (strings.Contains(err.Error(), "is busy:") || strings.Contains(err.Error(), "working on this jail")) {
		t.Fatalf("a copy after the operation: %v", err)
	}
}

func tarOf(t *testing.T, name, body string) io.Reader {
	t.Helper()
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	tw.Write([]byte(body))
	tw.Close()
	return &b
}
