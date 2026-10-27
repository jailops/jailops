//go:build live

package backupui_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"jailops.net/jailops/internal/auth"
	"jailops.net/jailops/internal/backupd"
	"jailops.net/jailops/internal/backupui"
	"jailops.net/jailops/internal/node"
	"jailops.net/jailops/internal/node/ctl"
	"jailops.net/jailops/internal/repo/datastore"
	"jailops.net/jailops/internal/testdb"
)

// TestLiveConsole serves the embedded backup console from a real jailops-backupd (directory datastore, embedded
// Postgres) over TLS on JAILOPS_LIVE_ADDR for a browser: go test -tags live -run TestLiveConsole ./internal/backupui
// Sign in as admin / "a long enough passphrase here".
func TestLiveConsole(t *testing.T) {
	addr := os.Getenv("JAILOPS_LIVE_ADDR")
	if addr == "" {
		t.Skip("JAILOPS_LIVE_ADDR not set")
	}
	ctx := context.Background()
	raw, err := datastore.NewDirBackend(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	db, err := ctl.Open(ctx, testdb.New(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	yaml := fmt.Sprintf("hostname: livenode\nlisten: 127.0.0.1:0\nadmin_socket: %s\ntls: {cert_file: %s, key_file: %s}\ndatabase: {dsn: \"postgres://unused\"}\ndatastore: {kind: directory, directory: %s}\nweb: {enabled: true}\ngrace: 12h\n",
		filepath.Join(dir, "s"), filepath.Join(dir, "c.crt"), filepath.Join(dir, "c.key"), dir)
	cfg, err := backupd.ParseConfig([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := cfg.EnsureServerCert(); err != nil {
		t.Fatal(err)
	}
	pp := auth.PasswordParams{MemoryKiB: 64, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32}
	if _, err := node.Init(ctx, raw, node.InitOptions{ServerID: "node-live", Host: "live"}); err != nil {
		t.Fatal(err)
	}
	cfg.ServerID = "node-live"
	srv, err := backupd.New(ctx, cfg, backupd.Options{Backend: raw, DB: db, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), PasswordParams: &pp, WebHandler: backupui.Handler()})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	if err := backupd.CreateUser(ctx, db, "admin", "a long enough passphrase here", "admin", pp); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewUnstartedServer(srv.WebHandler())
	ts.StartTLS()
	defer ts.Close()
	fmt.Printf("LIVE-URL %s\n", ts.URL)
	secs, _ := strconv.Atoi(os.Getenv("JAILOPS_LIVE_SECONDS"))
	if secs == 0 {
		secs = 300
	}
	time.Sleep(time.Duration(secs) * time.Second)
}
func TestMain(m *testing.M) { testdb.Main(m) }
