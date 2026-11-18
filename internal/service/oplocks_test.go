package service_test

import (
	"strings"
	"testing"
	"time"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/sdk/api"
)

// A client that sees an operation finish may start the next one at once: the locks of a stop are gone before its status
// is final, so a change of the jail right after `stop` never answers "busy". An operation that is still running does
// answer busy, and names itself.
func TestNextOperationRunsAtOnceAfterAStop(t *testing.T) {
	e, s := newEnv(t)
	ctx := as(globalAdmin, dtest.SeedProject)
	create, err := s.CreateJail(ctx, dtest.WebJail("web"))
	if err != nil {
		t.Fatal(err)
	}
	if got := wait(t, s, create); got.Status != api.OpSuccess {
		t.Fatalf("create: %+v\n%s", got, e.Log())
	}
	for i := 0; i < 40; i++ {
		start, err := s.JailAction(ctx, "web", api.JailAction{Action: "start"})
		if err != nil {
			t.Fatal(err)
		}
		if got := wait(t, s, start); got.Status != api.OpSuccess {
			t.Fatalf("start %d: %+v", i, got)
		}
		stop, err := s.JailAction(ctx, "web", api.JailAction{Action: "stop"})
		if err != nil {
			t.Fatal(err)
		}
		// while the stop is running a change answers busy and says what runs; once the stop is final it is accepted
		if _, err := s.ConfigureJail(ctx, "web", api.ConfigChange{Set: map[string]string{"boot.autostart": "true"}}, false); err != nil {
			wantCode(t, err, api.CodeBusy)
			if !strings.Contains(err.Error(), "Stop jail web is running") {
				t.Fatalf("busy does not name the stop: %v", err)
			}
		}
		// a client that polls the operation (the CLI does) acts the moment it reads the final status, before the daemon's own
		// bookkeeping of the operation is over
		for end := time.Now().Add(20 * time.Second); ; {
			got, err := s.GetOperation(ctx, stop.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status == api.OpSuccess {
				break
			}
			if got.Status == api.OpFailure || got.Status == api.OpCancelled || time.Now().After(end) {
				t.Fatalf("stop %d: %+v", i, got)
			}
			time.Sleep(time.Millisecond) // sleep-ok: the poll of a client that reads the status
		}
		op, err := s.ConfigureJail(ctx, "web", api.ConfigChange{Set: map[string]string{"boot.autostart": "false"}}, false)
		if err != nil {
			t.Fatalf("a change right after the stop %d finished was refused: %v", i, err)
		}
		if got := wait(t, s, op); got.Status != api.OpSuccess {
			t.Fatalf("change %d: %+v", i, got)
		}
	}
}
