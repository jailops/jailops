package cli_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"jailops.net/jailops/internal/cli"
	"jailops.net/jailops/sdk/fake"
)

const messagesLog = "Oct 29 11:00:00 web sshd[1]: Accepted publickey\n" +
	"Oct 29 11:30:00 web cron[2]: (root) CMD (job)\n" +
	"  continued from cron\n" +
	"Oct 29 11:50:00 web nginx[3]: Error: upstream timed out\n" +
	"Oct 29 11:55:00 web sshd[1]: Disconnected\n"

func seedLogs(h *harness) {
	h.srv.SeedJailLog("main", "web", "messages", []byte(messagesLog))
	h.srv.SeedJailLog("main", "web", "messages.0", []byte("old line\n"))
	h.srv.SeedJailLog("main", "web", "messages.1.gz", []byte("zz"))
	h.srv.SeedJailLog("main", "web", "nginx/error.log", []byte("2026/03/01 08:59:00 [error] upstream\n"))
}

// 'jail logs' lists and reads files; the plain output is the lines and nothing else.
func TestJailLogs(t *testing.T) {
	h := newHarness(t)
	h.seed()
	tr := h.transcript()
	tr.ok("jail", "create", "web", "--image", "freebsd-15@stable", "--network", "lan", "--storage", "default")
	tr.ok("jail", "logs", "web")
	seedLogs(h)
	tr.ok("jail", "logs", "web")
	tr.ok("jail", "logs", "web", "-o", "name")
	tr.ok("jail", "logs", "web", "-o", "json")
	tr.ok("jail", "logs", "web", "messages")
	tr.ok("jail", "logs", "web", "messages", "--tail", "2")
	tr.ok("jail", "logs", "web", "messages", "--grep", "SSHD")
	tr.ok("jail", "logs", "web", "messages", "-n", "1", "-o", "json")
	tr.ok("logs", "web", "nginx/error.log")
	tr.ok("logs", "web", "messages.0")
	tr.do("", "jail", "logs", "web", "messages.1.gz")
	tr.do("", "jail", "logs", "web", "nope")
	tr.do("", "jail", "logs", "web", "../etc/passwd")
	tr.do("", "jail", "logs", "web", "messages", "--tail", "0")
	tr.do("", "jail", "logs", "web", "messages", "--since", "yesterday")
	tr.do("", "jail", "logs", "web", "--events", "messages")
	tr.do("", "jail", "logs", "ghost")
	h.srv.SeedJailUnmounted("main", "web", true)
	tr.do("", "jail", "logs", "web")
	tr.do("", "jail", "logs", "web", "messages")
	tr.golden("jail_logs")

	// --since is evaluated by the server against its clock (the fake's is 2026-03-01 09:00)
	h.srv.SeedJailUnmounted("main", "web", false)
	h.srv.SeedJailLog("main", "web", "recent", []byte("Mar  1 08:30:00 web a\nMar  1 08:58:00 web b\n"))
	out, errs, code := h.run("", "jail", "logs", "web", "recent", "--since", "15m")
	if code != 0 || errs != "" || out != "Mar  1 08:58:00 web b\n" {
		t.Fatalf("since: %d %q %q", code, out, errs)
	}
	// an old server
	old := newHarness(t, fake.WithExtensions("operations", "etag", "events", "jails"))
	old.seed()
	old.run("", "jail", "create", "web", "--image", "freebsd-15@stable", "--network", "lan", "--storage", "default")
	if _, errs, code := old.run("", "jail", "logs", "web"); code != 6 || !strings.Contains(errs, `server does not support "jail_logs"`) {
		t.Fatalf("old server: %d %s", code, errs)
	}
}

// -f prints what is appended, continues at the offset of the previous reply
// and starts over after a rotation; it ends quietly when interrupted.
func TestJailLogsFollow(t *testing.T) {
	h := newHarness(t)
	h.seed()
	h.run("", "jail", "create", "web", "--image", "freebsd-15@stable", "--network", "lan", "--storage", "default")
	h.srv.SeedJailLog("main", "web", "messages", []byte("one\ntwo\n"))
	var out, errb syncBuilder
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	env := h.environ("", nil, nil)
	env.Stdout, env.Stderr = &out, &errb
	env.PollInterval = 5 * time.Millisecond
	done := make(chan int, 1)
	go func() {
		done <- cli.Execute(ctx, env, []string{"jail", "logs", "web", "messages", "-f", "--tail", "1"})
	}()
	waitFor(t, func() bool { return h.srv.LogReads() >= 2 })
	h.srv.SeedJailLog("main", "web", "messages", []byte("one\ntwo\nthree\n"))
	waitFor(t, func() bool { return strings.Contains(out.String(), "three") })
	h.srv.SeedJailLog("main", "web", "messages", []byte("fresh\n"))
	waitFor(t, func() bool { return strings.Contains(out.String(), "fresh") })
	cancel()
	if code := <-done; code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if got := out.String(); got != "two\nthree\nfresh\n" {
		t.Fatalf("output %q", got)
	}
	if !strings.Contains(errb.String(), "was rotated") {
		t.Fatalf("stderr %q", errb.String())
	}
}

// waitFor polls a condition the fake reaches on its own, briefly.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// syncBuilder is a strings.Builder two goroutines may share.
type syncBuilder struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuilder) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuilder) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// A line that holds escape sequences and other controls is printed with them
// replaced; -o json keeps the text as the server sent it.
func TestJailLogsControlCharacters(t *testing.T) {
	h := newHarness(t)
	h.seed()
	h.run("", "jail", "create", "web", "--image", "freebsd-15@stable", "--network", "lan", "--storage", "default")
	h.srv.SeedJailLog("main", "web", "evil", []byte("a\x1b]0;title\x07b\tc\x1b[2Jd\u009be\x7ff ü\n"))
	out, errs, code := h.run("", "jail", "logs", "web", "evil")
	if code != 0 || errs != "" || out != "a\uFFFD]0;title\uFFFDb\tc\uFFFD[2Jd\uFFFDe\uFFFDf ü\n" {
		t.Fatalf("%d %q %q", code, out, errs)
	}
	out, _, code = h.run("", "jail", "logs", "web", "evil", "-o", "json")
	if code != 0 || !strings.Contains(out, `\u001b]0;title\u0007b\tc\u001b[2Jd`+"\u009be\x7ff ü") {
		t.Fatalf("%d %s", code, out)
	}
}
