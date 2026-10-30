package run

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestExecBasics(t *testing.T) {
	ctx := context.Background()
	e := NewExec()
	res, err := e.Run(ctx, Cmd{Name: "echo", Args: []string{"-n", "a b; $(x)"}})
	if err != nil || string(res.Stdout) != "a b; $(x)" {
		t.Fatalf("echo: %q %v", res.Stdout, err)
	}
	res, err = e.Run(ctx, Cmd{Name: "sh", Args: []string{"-c", "echo oops >&2; exit 3"}})
	ee, ok := IsExit(err)
	if !ok || ee.Code != 3 || !strings.Contains(ee.Stderr, "oops") || res.ExitCode != 3 {
		t.Fatalf("exit: %v %+v", err, res)
	}
	_, err = e.Run(ctx, Cmd{Name: "definitely-not-a-command-xyz"})
	if err == nil {
		t.Fatal("want error for missing binary")
	}
}

// A command that exits while a process it started still holds its output
// (an rc script that starts a daemon) ends WaitDelay after its exit, with
// its own status.
func TestExecLeftoverHoldsOutput(t *testing.T) {
	ctx := context.Background()
	e := NewExec()
	res, err := e.Run(ctx, Cmd{Name: "sh", Args: []string{"-c", "sleep 5 & echo started"}, Timeout: 30 * time.Second})
	if err != nil || string(res.Stdout) != "started\n" || !res.Truncated {
		t.Fatalf("success: %q %v %+v", res.Stdout, err, res)
	}
	if res.Duration >= 5*time.Second {
		t.Fatalf("waited for the leftover process: %s", res.Duration)
	}
	_, err = e.Run(ctx, Cmd{Name: "sh", Args: []string{"-c", "sleep 5 & exit 4"}, Timeout: 30 * time.Second})
	if ee, ok := IsExit(err); !ok || ee.Code != 4 {
		t.Fatalf("failure: %v", err)
	}
}

func TestExecStdinStdout(t *testing.T) {
	var out bytes.Buffer
	_, err := NewExec().Run(context.Background(), Cmd{Name: "cat", Stdin: strings.NewReader("stream"), Stdout: &out})
	if err != nil || out.String() != "stream" {
		t.Fatalf("%q %v", out.String(), err)
	}
}

func TestExecTimeoutKillsGroup(t *testing.T) {
	start := time.Now()
	// The shell spawns a background sleep that holds stdout open; the group kill must reap it.
	_, err := NewExec().Run(context.Background(), Cmd{Name: "sh", Args: []string{"-c", "sleep 30 & sleep 30"}, Timeout: 200 * time.Millisecond})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("want ErrTimeout, got %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("took %s", d)
	}
}

func TestExecCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	_, err := NewExec().Run(ctx, Cmd{Name: "sleep", Args: []string{"30"}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestExecBoundedOutput(t *testing.T) {
	res, err := NewExec().Run(context.Background(), Cmd{Name: "sh", Args: []string{"-c", "head -c 100000 /dev/zero"}, MaxOutput: 1000})
	if err != nil || len(res.Stdout) != 1000 || !res.Truncated {
		t.Fatalf("len=%d trunc=%v err=%v", len(res.Stdout), res.Truncated, err)
	}
}

func TestFake(t *testing.T) {
	f := NewFake()
	f.On("zfs", []string{"list", AnyArg}, "x\n")
	f.OnFail("zfs", []string{"destroy", RestArgs}, 1, "busy")
	ctx := context.Background()
	if s, err := Stdout(ctx, f, Cmd{Name: "zfs", Args: []string{"list", "a"}}); err != nil || s != "x\n" {
		t.Fatal(s, err)
	}
	if _, err := f.Run(ctx, Cmd{Name: "zfs", Args: []string{"list"}}); err == nil {
		t.Fatal("arity must not match")
	}
	_, err := f.Run(ctx, Cmd{Name: "zfs", Args: []string{"destroy", "-r", "a"}, Stdin: strings.NewReader("in")})
	if ee, ok := IsExit(err); !ok || ee.Stderr != "busy" {
		t.Fatal(err)
	}
	if got := f.Calls()[2].StdinData; string(got) != "in" {
		t.Fatalf("stdin %q", got)
	}
	if len(f.Argv()) != 3 {
		t.Fatal(f.Argv())
	}
}

func TestRedact(t *testing.T) {
	tests := []struct {
		c    Cmd
		want string
	}{
		{Cmd{Name: "zfs", Args: []string{"list", "-H"}}, "zfs list -H"},
		{Cmd{Name: "zfs", Args: []string{"create", "-o", "keylocation=prompt", "a b"}}, "zfs create -o keylocation=*** 'a b'"},
		{Cmd{Name: "x", Args: []string{"--password", "hunter2", "ok"}}, "x --password *** ok"},
		{Cmd{Name: "x", Args: []string{"it's"}}, `x 'it'\''s'`},
		{Cmd{Name: "x", Args: []string{""}}, "x ''"},
	}
	for _, tc := range tests {
		if got := Redact(tc.c); got != tc.want {
			t.Errorf("got %q want %q", got, tc.want)
		}
	}
}
