package backupd

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"jailops.net/jailops/internal/node"
	"jailops.net/jailops/internal/schedule"
	"jailops.net/jailops/sdk/api"
)

// Scheduler runs the node's periodic work: it expires abandoned sessions every
// tick and starts retention, garbage collection and verification jobs at the
// times of the configuration. A job that finds another repository-wide job
// running is retried a few minutes later.
type Scheduler struct {
	n    *node.Node
	cfg  ScheduleConfig
	now  func() time.Time
	log  *slog.Logger
	tick time.Duration

	mu    sync.Mutex
	loc   *time.Location
	tasks []*task
}

type task struct {
	name string
	spec *schedule.Spec
	next time.Time
	run  func() error
}

func newScheduler(n *node.Node, cfg ScheduleConfig, now func() time.Time, log *slog.Logger) *Scheduler {
	if now == nil {
		now = time.Now
	}
	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		loc = time.UTC
	}
	s := &Scheduler{n: n, cfg: cfg, now: now, log: log, tick: 30 * time.Second, loc: loc}
	add := func(name, expr string, run func() error) {
		if expr == "" {
			return
		}
		spec, err := schedule.Parse(expr)
		if err != nil {
			log.Error("schedule: bad expression", "job", name, "err", err)
			return
		}
		t := &task{name: name, spec: spec, run: run}
		t.next, _ = spec.Next(now(), loc)
		s.tasks = append(s.tasks, t)
	}
	const by = "schedule"
	add("retention", cfg.Retention, func() error { _, err := n.StartRetention(node.RetentionOptions{}, by); return err })
	add("gc", cfg.GC, func() error { _, err := n.StartGC(node.GCOptions{}, by); return err })
	add("verify_new", cfg.VerifyNew, func() error {
		_, err := n.StartVerify(node.VerifyOptions{Mode: node.VerifyNew, Quarantine: true}, by)
		return err
	})
	add("verify_stale", cfg.VerifyStale, func() error {
		_, err := n.StartVerify(node.VerifyOptions{Mode: node.VerifyStale, StaleDays: cfg.StaleDays, Quarantine: true}, by)
		return err
	})
	add("verify_all", cfg.VerifyAll, func() error {
		_, err := n.StartVerify(node.VerifyOptions{Mode: node.VerifyAll, Quarantine: true}, by)
		return err
	})
	return s
}

// Next returns the next run time of each scheduled job, for status pages and tests.
func (s *Scheduler) Next() map[string]time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]time.Time{}
	for _, t := range s.tasks {
		out[t.name] = t.next
	}
	return out
}

// Step does one scheduler pass at the current time.
func (s *Scheduler) Step(ctx context.Context) {
	if _, err := s.n.Sweep(ctx); err != nil {
		s.log.Warn("sweeping expired sessions", "err", err)
	}
	_ = s.n.DB().PurgeUISessions(ctx)
	s.stepTasks(ctx)
}

func (s *Scheduler) stepTasks(ctx context.Context) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.tasks {
		if t.next.IsZero() || now.Before(t.next) {
			continue
		}
		err := t.run()
		switch {
		case err == nil:
			s.log.Info("scheduled job started", "job", t.name)
			t.next, _ = t.spec.Next(now, s.loc)
		case api.IsCode(err, api.CodeBusy):
			s.log.Info("scheduled job postponed: another job is running", "job", t.name)
			t.next = now.Add(5 * time.Minute)
		case api.IsCode(err, api.CodeMaintenance):
			s.log.Info("scheduled job skipped: maintenance mode", "job", t.name)
			t.next, _ = t.spec.Next(now, s.loc)
		default:
			s.log.Error("scheduled job failed to start", "job", t.name, "err", err)
			t.next, _ = t.spec.Next(now, s.loc)
		}
	}
}

// Run steps until ctx ends.
func (s *Scheduler) Run(ctx context.Context) {
	t := time.NewTicker(s.tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Step(ctx)
		}
	}
}
