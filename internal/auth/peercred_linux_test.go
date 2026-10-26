//go:build linux

package auth

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"jailops.net/jailops/sdk/api"
)

func TestPeerCredLinux(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "s")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	type res struct {
		c   Cred
		err error
	}
	ch := make(chan res, 1)
	go func() {
		conn, err := ln.AcceptUnix()
		if err != nil {
			ch <- res{err: err}
			return
		}
		defer conn.Close()
		c, err := PeerCred(conn)
		ch <- res{c, err}
	}()
	cl, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	r := <-ch
	if r.err != nil {
		t.Fatal(r.err)
	}
	if int(r.c.UID) != os.Getuid() || int(r.c.GID) != os.Getgid() || int(r.c.PID) != os.Getpid() {
		t.Fatalf("got %+v want uid=%d gid=%d pid=%d", r.c, os.Getuid(), os.Getgid(), os.Getpid())
	}
	// the dialing side sees the listener's credentials, which are ours too
	c2, err := PeerCred(cl)
	if err != nil || int(c2.UID) != os.Getuid() {
		t.Fatalf("%+v %v", c2, err)
	}
	if err := RequireUID(r.c, r.c.UID); err != nil {
		t.Fatal(err)
	}
	if err := RequireUID(r.c, r.c.UID+1); !api.IsCode(err, api.CodeForbidden) {
		t.Fatal(err)
	}
}
