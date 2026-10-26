package backupd

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"jailops.net/jailops/internal/auth"
	"jailops.net/jailops/internal/node"
	"jailops.net/jailops/internal/node/ctl"
	"jailops.net/jailops/sdk/api"
)

// The admin socket serves only root; everybody else is
// refused before a handler runs, whatever they ask for.
func TestAdminSocketRefusesOtherUsers(t *testing.T) {
	// a node is needed only for its handler table: build one on a nil database is not possible, so the
	// handler under test is the policy wrapper around a recording inner handler
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	h := policyHandler(inner)
	for _, c := range []struct {
		name string
		cred *auth.Cred
		want int
	}{
		{"no credentials", nil, 401},
		{"an ordinary user", &auth.Cred{UID: 1001, GID: 1001}, 403},
		{"a user of another group", &auth.Cred{UID: 1002, GID: 1002, Groups: []uint32{4242}}, 403},
		{"a user in group 0", &auth.Cred{UID: 1003, GID: 0, Groups: []uint32{0}}, 403},
		{"root", &auth.Cred{UID: 0, GID: 0}, 200},
	} {
		called = false
		req := httptest.NewRequest("POST", "/1.0/repo/admin/gc", nil)
		if c.cred != nil {
			req = req.WithContext(context.WithValue(req.Context(), credKey{}, *c.cred))
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.want || called != (c.want == 200) {
			t.Errorf("%s: %d (handler called: %v), want %d", c.name, rec.Code, called, c.want)
		}
	}
}

// policyHandler is the check adminHandler makes, with the inner handler supplied.
func policyHandler(inner http.Handler) http.Handler {
	return adminPolicy(nil, func() http.Handler { return inner })
}

func TestSchedulerRunsJobsAtTheirTimes(t *testing.T) {
	// the scheduler is exercised against a recording set of tasks: the node jobs themselves are tested elsewhere
	now := time.Date(2026, 6, 1, 3, 59, 0, 0, time.UTC)
	cfg := ScheduleConfig{Timezone: "UTC", GC: "0 4 * * *", VerifyNew: "30 4 * * *"}
	s := newScheduler(nil, cfg, func() time.Time { return now }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	var ran []string
	busy := true
	for _, tk := range s.tasks {
		name := tk.name
		tk.run = func() error {
			if name == "gc" && busy {
				return api.Errorf(api.CodeBusy, "busy")
			}
			ran = append(ran, name)
			return nil
		}
	}
	next := s.Next()
	if !next["gc"].Equal(time.Date(2026, 6, 1, 4, 0, 0, 0, time.UTC)) || !next["verify_new"].Equal(time.Date(2026, 6, 1, 4, 30, 0, 0, time.UTC)) {
		t.Fatalf("%v", next)
	}
	step := func() { s.stepTasks(context.Background()) }
	step()
	if len(ran) != 0 {
		t.Fatalf("ran early: %v", ran)
	}
	now = now.Add(2 * time.Minute) // 04:01
	step()
	if len(ran) != 0 || !s.Next()["gc"].Equal(now.Add(5*time.Minute)) {
		t.Fatalf("a busy job is retried in five minutes: ran %v next %v", ran, s.Next())
	}
	busy = false
	now = now.Add(6 * time.Minute) // 04:07
	step()
	if len(ran) != 1 || ran[0] != "gc" || !s.Next()["gc"].Equal(time.Date(2026, 6, 2, 4, 0, 0, 0, time.UTC)) {
		t.Fatalf("%v %v", ran, s.Next())
	}
	now = now.Add(30 * time.Minute) // 04:37
	step()
	if len(ran) != 2 || ran[1] != "verify_new" {
		t.Fatalf("%v", ran)
	}
	_ = node.Base
	_ = ctl.JobGC
}
