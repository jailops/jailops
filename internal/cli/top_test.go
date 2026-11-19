package cli_test

import (
	"context"
	"io"
	"regexp"
	"strings"
	"testing"
	"time"

	"jailops.net/jailops/internal/cli"
	"jailops.net/jailops/sdk/api"
	"jailops.net/jailops/sdk/fake"
)

func ptr[T any](v T) *T { return &v }

var topClockRe = regexp.MustCompile(`jailops top - \d\d:\d\d:\d\d`)

// noWait makes the waits of `jailops top` instant and counts them.
func noWait(t *testing.T) *int {
	t.Helper()
	n := 0
	restore := cli.SetTopSleep(func(ctx context.Context, _ time.Duration) error { n++; return ctx.Err() })
	t.Cleanup(restore)
	return &n
}

func topFrame() api.Top {
	return api.Top{
		MeasuredAt:      time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC),
		IntervalSeconds: 2,
		Host: &api.TopHost{Load1: 0.5, Load5: 0.4, Load15: 0.3, CPUs: 4, CPUPercent: ptr(7.5), MemoryTotal: 4 << 30, MemoryUsed: ptr(uint64(1 << 30)),
			ARCBytes: ptr(uint64(256 << 20)), Pool: "tank", PoolSize: ptr(uint64(100 << 30)), PoolAllocated: ptr(uint64(10 << 30)), PoolFree: ptr(uint64(90 << 30)), RunningJails: 3},
		Jails: []api.TopJail{
			{Project: "main", Name: "web", UptimeSeconds: 7380, CPUPercent: ptr(137.0), CPULimitPercent: 200, MemoryBytes: ptr(int64(407048192)), MemoryLimitBytes: 2 << 30,
				Processes: ptr(23), DiskBytes: ptr(int64(268435456)), DiskQuotaBytes: 10 << 30, NetInBytesPerSecond: ptr(int64(2048)), NetOutBytesPerSecond: ptr(int64(1536000))},
			{Project: "main", Name: "db", UptimeSeconds: 86400 * 3, CPUPercent: ptr(2.5), MemoryBytes: ptr(int64(1 << 30)), Processes: ptr(9), DiskBytes: ptr(int64(5 << 30)),
				NetInBytesPerSecond: ptr(int64(100)), NetOutBytesPerSecond: ptr(int64(50))},
			{Project: "shop", Name: "cache", Approximate: true, CPUPercent: ptr(0.0), MemoryBytes: ptr(int64(12 << 20)), Processes: ptr(3)},
		},
		Notes: []string{"racct is off: cpu and memory are approximate (from the process list); kern.racct.enable=1 in /boot/loader.conf and a reboot give exact numbers"},
	}
}

// One frame in every format, with the table sorted by the key and the project column only when there are several.
func TestTopOnce(t *testing.T) {
	noWait(t)
	h := newHarness(t)
	h.srv.SeedTop(topFrame())
	var b strings.Builder
	for _, args := range [][]string{
		{"top", "--once"},
		{"top", "--once", "--sort", "mem"},
		{"top", "--once", "--sort", "name"},
		{"top", "--once", "--sort", "net"},
		{"top", "--once", "-o", "json"},
		{"top", "--once", "-o", "yaml"},
		{"top", "--once", "-o", "name"},
		{"top", "--once", "-o", "csv"},
		{"top"}, // not a terminal: one frame
	} {
		out, errs, code := h.run("", args...)
		if code != 0 || errs != "" {
			t.Fatalf("%v: %d %s", args, code, errs)
		}
		b.WriteString("$ jailops " + strings.Join(args, " ") + "\n" + topClockRe.ReplaceAllString(out, "jailops top - <time>") + "\n")
	}
	golden(t, "top", b.String())
}

// Only one project in the frame: no project column; no host numbers for a project identity: the one-line header.
func TestTopOneProjectNoHost(t *testing.T) {
	noWait(t)
	h := newHarness(t)
	f := topFrame()
	f.Host, f.Jails, f.Notes = nil, f.Jails[:2], nil
	h.srv.SeedTop(f)
	out, _, code := h.run("", "top", "--once")
	if code != 0 {
		t.Fatal(out)
	}
	if strings.Contains(out, "PROJECT") || strings.Contains(out, "load ") || !strings.Contains(out, "2 running jails") {
		t.Fatalf("%s", out)
	}
	// an empty host says so
	f.Jails = nil
	h.srv.SeedTop(f)
	out, _, _ = h.run("", "top", "--once")
	if !strings.Contains(out, "no running jail") {
		t.Fatalf("%s", out)
	}
}

func TestTopRefusesWhatItCannotDo(t *testing.T) {
	noWait(t)
	h := newHarness(t)
	for args, want := range map[string]string{
		"top --sort speed":        "--sort is one of cpu, mem, disk, net, name",
		"top --interval 500ms":    "the interval is at least 1s",
		"top -l broken --once":    "label",
		"top extra":               "unknown command",
		"top --once --interval 0": "the interval is at least 1s",
	} {
		_, errs, code := h.run("", strings.Fields(args)...)
		if code == 0 || !strings.Contains(errs, want) {
			t.Errorf("%s: %d %q (want %q)", args, code, errs, want)
		}
	}
	// a server that does not know the route says so
	old := newHarness(t, fake.WithExtensions("operations", "etag", "events", "jails", "profiles", "volumes"))
	_, errs, code := old.run("", "top", "--once")
	if code == 0 || !strings.Contains(errs, `server does not support "top"`) {
		t.Fatalf("%d %q", code, errs)
	}
}

// Frames from the real jails of the fake: the project of the harness, or every project with the default.
func TestTopAsksForTheWholeHostUnlessAProjectIsNamed(t *testing.T) {
	noWait(t)
	h := newHarness(t)
	h.seed()
	h.srv.SeedProject(api.Project{Metadata: api.ObjectMeta{Name: "shop"}, Spec: api.ProjectSpec{Storage: []api.ProjectStorage{{Storage: "default"}}}})
	for _, args := range [][]string{{"create", "web", "--image", "freebsd-15", "--storage", "default", "--network", "lan", "--start"}} {
		if _, errs, code := h.run("", args...); code != 0 {
			t.Fatalf("%v: %s", args, errs)
		}
	}
	out, errs, code := h.run("", "top", "--once", "-o", "name")
	if code != 0 || strings.TrimSpace(out) != "web" {
		t.Fatalf("%q %q %d", out, errs, code)
	}
	if out, _, _ := h.run("", "top", "--once", "-o", "name", "--project", "shop"); strings.TrimSpace(out) != "" {
		t.Fatalf("another project has no jail: %q", out)
	}
	out, _, _ = h.run("", "top", "--once", "-l", "role=db", "-o", "name")
	if strings.TrimSpace(out) != "" {
		t.Fatalf("a label that no jail has: %q", out)
	}
}

// A stream of frames in JSON: the first is measured before anything is printed, then one per interval, until the
// command is stopped.
func TestTopStreamsJSONAndYAML(t *testing.T) {
	n := 0
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	restore := cli.SetTopSleep(func(c context.Context, _ time.Duration) error {
		n++
		if n > 3 {
			cancel()
			return context.Canceled
		}
		return nil
	})
	t.Cleanup(restore)
	h := newHarness(t)
	h.srv.SeedTop(topFrame())
	h.ctx = ctx
	out, errs, code := h.run("", "top", "-o", "json")
	if code != 0 || errs != "" {
		t.Fatalf("%d %s", code, errs)
	}
	if got := strings.Count(out, `"measured_at"`); got != 3 {
		t.Fatalf("%d frames:\n%s", got, out)
	}
	n = 0
	ctx2, cancel2 := context.WithCancel(context.Background())
	t.Cleanup(cancel2)
	h.ctx = ctx2
	restore2 := cli.SetTopSleep(func(c context.Context, _ time.Duration) error {
		n++
		if n > 2 {
			cancel2()
			return context.Canceled
		}
		return nil
	})
	t.Cleanup(restore2)
	out, _, code = h.run("", "top", "-o", "yaml")
	if code != 0 || strings.Count(out, "measured_at:") != 2 || strings.Count(out, "\n---\n") != 1 {
		t.Fatalf("%d:\n%s", code, out)
	}
}

// On a terminal the table redraws, and the keys sort it and end it.
func TestTopLiveKeys(t *testing.T) {
	restore := cli.SetTopSleep(func(ctx context.Context, _ time.Duration) error { <-ctx.Done(); return ctx.Err() })
	t.Cleanup(restore)
	h := newHarness(t)
	h.tty = true
	h.rawTerm = func(any) (func(), error) { return func() {}, nil }
	h.srv.SeedTop(topFrame())
	h.stdin = io.NopCloser(strings.NewReader("zdnq"))
	out, errs, code := h.run("", "top")
	if code != 0 || errs != "" {
		t.Fatalf("%d %s", code, errs)
	}
	for _, want := range []string{"\x1b[?1049h", "\x1b[?1049l", "sorted by cpu", "sorted by disk", "sorted by net", "c cpu  m mem  d disk  n net  a name  q quit"} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in:\n%q", want, out)
		}
	}
	if strings.Contains(out, "sorted by z") {
		t.Fatal("an unknown key changes nothing")
	}
	// a server that does not know the route ends the live view with the reason instead of drawing nothing
	old := newHarness(t, fake.WithExtensions("operations", "etag", "events", "jails", "profiles", "volumes"))
	old.tty = true
	old.rawTerm = func(any) (func(), error) { return func() {}, nil }
	_, errs, code = old.run("", "top")
	if code == 0 || !strings.Contains(errs, `does not support "top"`) {
		t.Fatalf("%d %q", code, errs)
	}
}
