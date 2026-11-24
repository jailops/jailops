package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"jailops.net/jailops/internal/sys/jailos"
)

// A jail that already runs when the daemon starts gets its start time from the oldest process in it; a jail the daemon
// started itself keeps the time it recorded, and a jail ps cannot read stays without one.
func TestRunningJailsGetTheirStartTimeAtDaemonStart(t *testing.T) {
	s := cpusetService(t, "4", nil, pinnedJail("web", ""), pinnedJail("db", ""), pinnedJail("cache", ""))
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	jf := jailos.NewFake()
	jf.Put(jailos.Jail{JID: 7, Name: "alpha_web"})
	jf.Put(jailos.Jail{JID: 8, Name: "alpha_db"})
	jf.Put(jailos.Jail{JID: 9, Name: "alpha_cache"})
	s.d.Jails = jf
	s.d.HostExec = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "ps" || len(args) != 5 || args[3] != "-o" || args[4] != "etimes=" {
			t.Errorf("%s %v", name, args)
		}
		switch args[2] {
		case "7":
			return []byte("   120\n  3600\n     5\n"), nil
		case "8":
			t.Error("the start time the daemon recorded is asked again")
		}
		return nil, errors.New("ps: exit 1")
	}
	mine := now.Add(-time.Minute)
	s.status["alpha_db"] = jailRuntime{startedAt: mine}
	s.recoverStartTimes(context.Background())
	if got := s.status["alpha_web"].startedAt; !got.Equal(now.Add(-time.Hour)) {
		t.Fatalf("web: %v", got)
	}
	if got := s.status["alpha_db"].startedAt; !got.Equal(mine) {
		t.Fatalf("db: %v", got)
	}
	if got := s.status["alpha_cache"].startedAt; !got.IsZero() {
		t.Fatalf("cache: %v", got)
	}
}
