package priv

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"jailops.net/jailops/internal/dnsprobe"
	"jailops.net/jailops/internal/sys/run"
)

// dnsRunner stands in for the control program of unbound: every odd reload leaves the resolver without its loopback
// (the file it read took it away), every even one (the rollback) gives it back.
type dnsRunner struct {
	mu      sync.Mutex
	reloads int
	down    bool
}

func (r *dnsRunner) Run(_ context.Context, c run.Cmd) (run.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c.Name == "unbound-control" && len(c.Args) > 0 && c.Args[0] == "reload" {
		r.reloads++
		r.down = r.reloads%2 == 1
	}
	return run.Result{}, nil
}

func (r *dnsRunner) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reloads
}

type dnsEnv struct {
	l    *Local
	r    *dnsRunner
	path string
	now  time.Time
	// answer is what the probe says: "" follows the runner (down after the first reload), else a fixed status
	answer dnsprobe.Status
	fixed  bool
}

func newDNSEnv(t *testing.T) *dnsEnv {
	t.Helper()
	e := newEnv(t)
	oldUID, oldAnchor := trustUID, trustAnchor
	trustUID = uint32(os.Getuid())
	t.Cleanup(func() { trustUID, trustAnchor = oldUID, oldAnchor })
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "unbound-control"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	confd := filepath.Join(e.base, "unbound", "conf.d")
	if err := os.MkdirAll(confd, 0o755); err != nil {
		t.Fatal(err)
	}
	etc := filepath.Join(e.base, "hostetc")
	if err := os.MkdirAll(etc, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(etc, "resolv.conf"), []byte("nameserver 127.0.0.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.cfg.UnboundConf = filepath.Join(confd, "jailops.conf")
	d := &dnsEnv{r: &dnsRunner{}, path: e.cfg.UnboundConf, now: time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)}
	if err := os.WriteFile(d.path, []byte("OLD\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	probe := func(context.Context, string, string, uint16, time.Duration) dnsprobe.Result {
		if d.fixed {
			return dnsprobe.Result{Status: d.answer}
		}
		d.r.mu.Lock()
		defer d.r.mu.Unlock()
		if d.r.down {
			return dnsprobe.Result{}
		}
		return dnsprobe.Result{Status: dnsprobe.Answered}
	}
	l, err := NewLocal(e.cfg, Deps{ZFS: e.zfs, Jails: e.jails, Net: e.net, Runner: d.r, Etc: etc, DNSProbe: probe, Now: func() time.Time { return d.now }}, WithoutChown())
	if err != nil {
		t.Fatal(err)
	}
	d.l = l
	return d
}

func (d *dnsEnv) read(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(d.path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A resolver that exits 0 from its reload and then does not answer on the loopback is not a success: the old file goes
// back, the caller gets the reason, and the same text is not tried again at every pass.
func TestDNSUnboundRollsBackWhenTheHostResolverStopsAnswering(t *testing.T) {
	d := newDNSEnv(t)
	ctx := WithAdmin(context.Background(), "root")
	res, err := d.l.DNSUnbound(ctx, true)
	if err == nil || !strings.Contains(err.Error(), "stopped answering at 127.0.0.1") {
		t.Fatalf("err %v, res %+v", err, res)
	}
	if got := d.read(t); got != "OLD\n" || res.Changed || res.Current {
		t.Fatalf("the old file is back: %q, %+v", got, res)
	}
	if n := d.r.count(); n != 2 { // the apply and the rollback
		t.Fatalf("%d reloads", n)
	}
	// the same text again: held back, nothing is touched
	res, err = d.l.DNSUnbound(ctx, true)
	if err != nil || !strings.Contains(res.Control, "held back") || d.r.count() != 2 || d.read(t) != "OLD\n" {
		t.Fatalf("second call: err %v, %+v, %d reloads", err, res, d.r.count())
	}
	// ten minutes later it is tried again
	d.now = d.now.Add(11 * time.Minute)
	if _, err = d.l.DNSUnbound(ctx, true); err == nil || d.r.count() != 4 {
		t.Fatalf("third call after the hold: err %v, %d reloads", err, d.r.count())
	}
}

// An address that did not answer before the change does not answer after it either: that is not the change's doing.
// The names stay, and the silence is a warning (a resolver that REFUSES the host's queries is such an address).
func TestDNSUnboundKeepsTheNamesWhenNothingAnsweredBefore(t *testing.T) {
	d := newDNSEnv(t)
	d.fixed, d.answer = true, dnsprobe.Refused
	res, err := d.l.DNSUnbound(WithAdmin(context.Background(), "root"), true)
	if err != nil || !res.Changed || !strings.Contains(res.Warn, "does not answer at 127.0.0.1") || d.read(t) == "OLD\n" {
		t.Fatalf("err %v, %+v", err, res)
	}
}
