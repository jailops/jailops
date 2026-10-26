package daemon_test

import (
	"crypto/tls"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"jailops.net/jailops/internal/daemon"
	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/internal/webui"
)

func TestWebConsoleIsOptional(t *testing.T) {
	cfg, err := daemon.Decode(strings.NewReader("pool: tank\n"))
	if err != nil || !cfg.UI() {
		t.Fatalf("the console is on by default: %v %v", cfg.UI(), err)
	}
	cfg, err = daemon.Decode(strings.NewReader("pool: tank\nweb:\n  enabled: false\n"))
	if err != nil || cfg.UI() {
		t.Fatalf("web.enabled false must switch the console off: %v %v", cfg.UI(), err)
	}

	// With the console off the daemon is the API and /healthz and nothing else.
	off := dtest.New(t, dtest.Options{Mutate: func(c *daemon.Config) { f := false; c.Web.Enabled = &f }})
	on := dtest.New(t, dtest.Options{})
	if !webui.Enabled {
		if code, body := on.Raw("GET", "/", "", ""); code != 404 || !strings.Contains(body, "no web console") {
			t.Fatalf("a noweb build: %d %q", code, body)
		}
	} else if code, body := on.Raw("GET", "/", "", ""); code == 404 && !strings.Contains(body, "web UI not built") || code != 404 && !strings.Contains(body, "<html") {
		t.Fatalf("console on: %d %q", code, body)
	}
	if code, body := off.Raw("GET", "/", "", ""); code != 404 || strings.Contains(body, "web UI") || strings.Contains(body, "<html") {
		t.Fatalf("console off: %d %q", code, body)
	}
	if code, _ := off.Raw("GET", "/healthz", "", ""); code != 200 {
		t.Fatalf("healthz: %d", code)
	}
	if code, _ := off.Raw("GET", "/1.0", "", ""); code != 200 {
		t.Fatalf("API without the console: %d", code)
	}
	if _, err := off.Admin.Info(t.Context()); err != nil {
		t.Fatalf("the SDK without the console: %v", err)
	}
}

func TestListenAddressIsValidated(t *testing.T) {
	for addr, ok := range map[string]bool{
		":8443": true, "127.0.0.1:8443": true, "[::1]:8443": true, "host.example.org:8443": true, "": true,
		"8443": false, "http://0.0.0.0:80": false, ":99999": false, "1.2.3.4": false, ":port": false, "bad host:1": false,
	} {
		_, err := daemon.Decode(strings.NewReader("pool: tank\nlisten:\n  https:\n    address: \"" + addr + "\"\n"))
		if (err == nil) != ok {
			t.Errorf("address %q: err=%v want ok=%v", addr, err, ok)
		}
	}
	for name, doc := range map[string]string{
		"cert without key": "pool: tank\nlisten:\n  https:\n    cert: /etc/ssl/s.crt\n",
		"key without cert": "pool: tank\nlisten:\n  https:\n    key: /etc/ssl/s.key\n",
		"relative cert":    "pool: tank\nlisten:\n  https:\n    cert: s.crt\n    key: s.key\n",
	} {
		if _, err := daemon.Decode(strings.NewReader(doc)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := daemon.Decode(strings.NewReader("pool: tank\nlisten:\n  https:\n    address: \":8443\"\n    cert: /etc/ssl/s.crt\n    key: /etc/ssl/s.key\n")); err != nil {
		t.Fatal(err)
	}
}

// The listener only ever speaks TLS 1.3: there is no plain-http TCP listener to refuse.
func TestHTTPSListenerIsTLS13Only(t *testing.T) {
	e := dtest.New(t, dtest.Options{Listen: true})
	addr := e.D.HTTPSAddr()
	if addr == "" || !strings.HasPrefix(addr, "127.0.0.1:") {
		t.Fatalf("addr %q", addr)
	}
	cl := &http.Client{Timeout: 5 * time.Second}
	if resp, err := cl.Get("http://" + addr + "/healthz"); err == nil {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == 200 && string(b) == "ok\n" {
			t.Fatal("the TCP listener answered plain http")
		}
	}
	old := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MaxVersion: tls.VersionTLS12}}}
	if _, err := old.Get("https://" + addr + "/healthz"); err == nil {
		t.Fatal("TLS 1.2 was accepted")
	}
	ok := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	resp, err := ok.Get("https://" + addr + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

// An operator's certificate is served as given, never regenerated, and a renewed one is picked up without a restart.
func TestOperatorCertificateIsServedAndReloaded(t *testing.T) {
	dir := t.TempDir()
	crt, key := filepath.Join(dir, "own.crt"), filepath.Join(dir, "own.key")
	writeCert(t, crt, key, "first.example.test")
	// the daemon's goroutines read the clock while the test moves it: an atomic, not a plain variable
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	e := dtest.New(t, dtest.Options{Listen: true, Now: func() time.Time { return time.Unix(0, clock.Load()) }, Mutate: func(c *daemon.Config) {
		c.Listen.HTTPS.Cert, c.Listen.HTTPS.Key = crt, key
	}})
	if got := serverName(t, e.D.HTTPSAddr()); got != "first.example.test" {
		t.Fatalf("served %q", got)
	}
	// Renewal: new files on disk. Nothing changes until the reload interval has passed.
	writeCert(t, crt, key, "second.example.test")
	if got := serverName(t, e.D.HTTPSAddr()); got != "first.example.test" {
		t.Fatalf("reloaded before the interval: %q", got)
	}
	clock.Add(int64(time.Minute))
	if got := serverName(t, e.D.HTTPSAddr()); got != "second.example.test" {
		t.Fatalf("not reloaded: %q", got)
	}
	// A half-written renewal keeps the certificate in use.
	if err := os.WriteFile(crt, []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	clock.Add(int64(time.Minute))
	if got := serverName(t, e.D.HTTPSAddr()); got != "second.example.test" {
		t.Fatalf("a broken renewal replaced the certificate: %q", got)
	}
	if !strings.Contains(e.Log(), "tls certificate reloaded") || !strings.Contains(e.Log(), "does not load") {
		t.Fatalf("log:\n%s", e.Log())
	}
}
