package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"jailops.net/jailops/sdk/api"
)

// installingJail is a jail whose bash is being installed, in the given step, since the given time.
func installingJail(step string, since time.Time) api.Jail {
	return api.Jail{Status: api.JailStatus{Conditions: []api.Condition{{Type: "ShellSetup", Status: api.ConditionUnknown,
		Reason: api.ShellInstalling, Since: since, Message: api.ShellInstallingMessage(step)}}}}
}

// The wait says what it waits for and how to leave, then shows the step and the seconds; off a terminal it adds a
// line every five seconds, not one per poll.
func TestShellWaitShowsWhatItWaitsFor(t *testing.T) {
	clock := time.Date(2026, 11, 18, 3, 0, 0, 0, time.UTC)
	since := clock
	polls := 0
	var out bytes.Buffer
	w := shellWait{out: &out, now: func() time.Time { return clock },
		sleep: func(ctx context.Context, d time.Duration) error { clock = clock.Add(d); return nil },
		get: func(ctx context.Context) (api.Jail, error) {
			polls++
			if polls > 13 { // 12 s of waiting
				return api.Jail{}, nil
			}
			step := "fetching and installing bash with pkg (the package catalogue comes first)"
			if polls > 9 {
				step = "updating the library cache of the jail"
			}
			return installingJail(step, since), nil
		},
		poll: time.Second, limit: time.Minute}
	if err := w.run(context.Background(), "mailli"); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"installing the shell (bash) in the jail; Ctrl-C stops waiting, `jailops exec mailli -- sh` works meanwhile",
		"fetching and installing bash with pkg (the package catalogue comes first) (0 s)",
		"(5 s)", "(10 s)", "updating the library cache of the jail",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if n := strings.Count(got, "\n"); n > 5 {
		t.Errorf("%d lines for a wait of twelve seconds off a terminal:\n%s", n, got)
	}
}

// On a terminal the line is rewritten in place, and the wait leaves it clean.
func TestShellWaitRewritesOneLineOnATerminal(t *testing.T) {
	clock := time.Date(2026, 11, 18, 3, 0, 0, 0, time.UTC)
	polls := 0
	var out bytes.Buffer
	w := shellWait{out: &out, tty: true, now: func() time.Time { return clock },
		sleep: func(ctx context.Context, d time.Duration) error { clock = clock.Add(d); return nil },
		get: func(ctx context.Context) (api.Jail, error) {
			if polls++; polls > 3 {
				return api.Jail{}, nil
			}
			return installingJail("writing the prompt", clock), nil
		},
		poll: time.Second, limit: time.Minute}
	if err := w.run(context.Background(), "web"); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if strings.Count(got, "\r\033[K") < 3 || !strings.HasSuffix(got, "\r\033[K") {
		t.Errorf("the status line is not rewritten in place and cleared:\n%q", got)
	}
}

// Ctrl-C ends the wait and says that the setup goes on: the wait only asks the daemon, it never stops anything.
func TestShellWaitCtrlCStopsOnlyTheWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var out bytes.Buffer
	asked := 0
	w := shellWait{out: &out, now: time.Now,
		sleep: func(ctx context.Context, d time.Duration) error { cancel(); return ctx.Err() },
		get: func(ctx context.Context) (api.Jail, error) {
			asked++
			return installingJail("writing the prompt", time.Now()), nil
		},
		poll: time.Second, limit: time.Minute}
	err := w.run(ctx, "web")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out.String(), "stopped waiting; the setup goes on in the jail") {
		t.Errorf("no word about the setup:\n%s", out.String())
	}
	if asked != 1 {
		t.Errorf("the jail was asked %d times: the wait asks once before it sleeps and never acts", asked)
	}
}

// A jail that is not installing its bash costs the shell no wait and no line.
func TestShellWaitSaysNothingWhenThereIsNothingToWaitFor(t *testing.T) {
	var out bytes.Buffer
	w := shellWait{out: &out, now: time.Now, sleep: sleepCtx, poll: time.Second, limit: time.Minute,
		get: func(ctx context.Context) (api.Jail, error) { return api.Jail{}, nil }}
	if err := w.run(context.Background(), "web"); err != nil || out.Len() != 0 {
		t.Fatalf("err %v, output %q", err, out.String())
	}
}
