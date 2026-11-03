package priv

import (
	"context"
	"io"
	"runtime"
	"strings"
	"testing"
	"time"
)

// A short command run with a terminal must deliver its whole output however fast it exits (the output of `echo hi`
// was lost in about half of the runs on FreeBSD when the parent closed the slave at once).
func TestTTYSessionDeliversTheOutputOfAShortCommand(t *testing.T) {
	if runtime.GOOS == "freebsd" {
		t.Skip("known: FreeBSD hangs the terminal up when the session leader exits and drops the output not yet read (3 to 30% of the runs of `echo hi`; docs/design.md section 16); a pty without a controlling terminal and with the slave kept open lost none in 300 runs, but an interactive shell needs the controlling terminal")
	}
	const runs = 300
	lost := 0
	for i := 0; i < runs; i++ {
		s, err := startSession(context.Background(), "/bin/echo", []string{"hello-from-the-tty"}, ExecOpts{TTY: true}, func() {})
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan string, 1)
		go func() { b, _ := io.ReadAll(s.Stdout()); done <- string(b) }()
		_, _ = s.Wait()
		select {
		case out := <-done:
			if !strings.Contains(out, "hello-from-the-tty") {
				lost++
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the output never ended")
		}
		_ = s.Close()
	}
	if lost > 0 {
		t.Fatalf("the output was lost in %d of %d runs", lost, runs)
	}
}
