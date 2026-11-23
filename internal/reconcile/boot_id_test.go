package reconcile_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"jailops.net/jailops/internal/reconcile"
)

func bootReading(sec int64) string {
	return fmt.Sprintf("{ sec = %d, usec = 1 } boot", sec)
}

func bootRec(t *testing.T, record string, recorded time.Time, now time.Time, bootSec int64) *reconcile.Reconciler {
	t.Helper()
	f := filepath.Join(t.TempDir(), "boot-id")
	if record != "" {
		if err := os.WriteFile(f, []byte(record+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(f, recorded, recorded); err != nil {
			t.Fatal(err)
		}
	}
	return reconcile.New(reconcile.Config{BootStateFile: f}, reconcile.Env{
		Sysctl: func(context.Context, string) (string, error) { return bootReading(bootSec), nil },
		Clock:  &fakeClock{now: now}})
}

// A host that reboots twice within ten minutes is a new boot for the jails that start with the host (the readings of
// kern.boottime are inside the tolerance for a clock that was set).
func TestHostThatRebootedTwiceInTenMinutesBootsItsJails(t *testing.T) {
	now := time.Date(2026, 11, 23, 19, 56, 35, 0, time.UTC)
	first := now.Add(-9 * time.Minute) // the first boot and the record of it
	r := bootRec(t, bootReading(first.Unix()), first.Add(20*time.Second), now, now.Add(-8*time.Second).Unix())
	if !r.HostBootedForTest(context.Background()) {
		t.Fatal("a second boot nine minutes after the first was taken for the same boot: no jail would start")
	}
}

// A clock that is set during a boot moves kern.boottime; that is not a boot.
func TestClockStepIsNotABoot(t *testing.T) {
	now := time.Date(2026, 11, 23, 19, 56, 35, 0, time.UTC)
	boot := now.Add(-2 * time.Hour)
	// recorded an hour ago, the clock was set five minutes forward since: the reading moved, the host stayed up
	r := bootRec(t, bootReading(boot.Unix()), now.Add(-time.Hour), now, boot.Add(5*time.Minute).Unix())
	if r.HostBootedForTest(context.Background()) {
		t.Fatal("a clock step was taken for a boot: the daemon would start jails the administrator stopped")
	}
}

func TestSameBootReadingIsNotABoot(t *testing.T) {
	now := time.Date(2026, 11, 23, 19, 56, 35, 0, time.UTC)
	boot := now.Add(-3 * time.Hour)
	r := bootRec(t, bootReading(boot.Unix()), boot.Add(30*time.Second), now, boot.Unix())
	if r.HostBootedForTest(context.Background()) {
		t.Fatal("a daemon that started again on a host that stayed up booted its jails")
	}
}

func TestFirstStartBoots(t *testing.T) {
	now := time.Date(2026, 11, 23, 19, 56, 35, 0, time.UTC)
	r := bootRec(t, "", now, now, now.Add(-time.Minute).Unix())
	if !r.HostBootedForTest(context.Background()) {
		t.Fatal("the first start of the daemon did not boot its jails")
	}
}
