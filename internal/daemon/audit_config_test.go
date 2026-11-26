package daemon_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"jailops.net/jailops/internal/daemon"
	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/internal/store"
)

func TestAuditRetentionConfig(t *testing.T) {
	cfg, err := daemon.Decode(strings.NewReader("pool: tank\n"))
	if err != nil || cfg.Audit.Retention.Std() != 90*24*time.Hour || cfg.Audit.MaxEntries != 1000000 {
		t.Fatalf("defaults: %+v %v", cfg.Audit, err)
	}
	cfg, err = daemon.Decode(strings.NewReader("pool: tank\naudit:\n  retention: 30d\n  max_entries: 5000\n"))
	if err != nil || cfg.Audit.Retention.Std() != 30*24*time.Hour || cfg.Audit.MaxEntries != 5000 {
		t.Fatalf("set: %+v %v", cfg.Audit, err)
	}
	for _, bad := range []string{"retention: 12h", "max_entries: 10", "retention: -1d", "retention: 5x"} {
		if _, err := daemon.Decode(strings.NewReader("pool: tank\naudit:\n  " + bad + "\n")); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

// The daily prune follows the configuration and the daemon's clock.
func TestPruneAuditFollowsTheClock(t *testing.T) {
	var mu sync.Mutex
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	e := dtest.New(t, dtest.Options{Now: clock, Mutate: func(c *daemon.Config) { c.Audit.Retention = daemon.Duration(24 * time.Hour) }})
	db := e.D.Service().DB()
	ctx := context.Background()
	for i := range 1500 {
		if _, err := db.Audit.Append(ctx, store.AuditEntry{Actor: "a", Method: "POST", Route: "/r", Result: "ok", Time: now.Add(time.Duration(i) * time.Millisecond)}); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := e.D.PruneAudit(ctx); err != nil || n != 0 {
		t.Fatalf("a fresh log: %d %v", n, err)
	}
	mu.Lock()
	now = now.Add(48 * time.Hour)
	mu.Unlock()
	n, err := e.D.PruneAudit(ctx)
	if err != nil || n < 500 {
		t.Fatalf("after two days: %d %v", n, err)
	}
	if l, _ := db.Audit.List(ctx, store.AuditFilter{Limit: 10000}); len(l) != store.AuditKeep {
		t.Fatalf("%d entries left", len(l))
	}
}

// A jailopsd.yaml of 0.9.4 may carry the keys of the removed application logs; the daemon still starts.
func TestConfigDropsRemovedAppLogKeys(t *testing.T) {
	for _, v := range []string{"true", "false"} {
		in := "pool: tank\napps:\n  catalog_dir: /x\nlogging:\n  forward:\n    - name: a\n      type: syslog\n      to: \"tls://h:6514\"\n      sources:\n        no_app_logs: " + v + "\n"
		if _, err := daemon.Decode(strings.NewReader(in)); err != nil {
			t.Errorf("no_app_logs %s: %v", v, err)
		}
	}
	if _, err := daemon.Decode(strings.NewReader("pool: tank\nlogging:\n  forward:\n    - name: a\n      type: syslog\n      to: \"tls://h:6514\"\n      sources:\n        bogus: 1\n")); err == nil {
		t.Error("an unknown key under sources was accepted")
	}
}

// exec_idle and exec_max are opt-in: unset or 0 is no limit, a negative value is an error.
func TestExecLimitsConfig(t *testing.T) {
	cfg, err := daemon.Decode(strings.NewReader("pool: tank\n"))
	if err != nil || cfg.Limits.ExecIdle != 0 || cfg.Limits.ExecMax != 0 {
		t.Fatalf("defaults: %+v %v", cfg.Limits, err)
	}
	cfg, err = daemon.Decode(strings.NewReader("pool: tank\nlimits:\n  exec_idle: 0\n  exec_max: 0s\n"))
	if err != nil || cfg.Limits.ExecIdle != 0 || cfg.Limits.ExecMax != 0 {
		t.Fatalf("zero: %+v %v", cfg.Limits, err)
	}
	cfg, err = daemon.Decode(strings.NewReader("pool: tank\nlimits:\n  exec_idle: 30m\n  exec_max: 12h\n"))
	if err != nil || cfg.Limits.ExecIdle.Std() != 30*time.Minute || cfg.Limits.ExecMax.Std() != 12*time.Hour {
		t.Fatalf("set: %+v %v", cfg.Limits, err)
	}
	for _, bad := range []string{"exec_idle: -1m", "exec_max: -1h", "exec_idle: -1d"} {
		if _, err := daemon.Decode(strings.NewReader("pool: tank\nlimits:\n  " + bad + "\n")); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}
