package server_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/sdk/api"
)

// The previous-client test is how a change of the wire that the clients in use cannot follow gets caught: a program
// built with the client library of an earlier release (testdata/prevsdk, made by scripts/prev-sdk.sh) runs the calls a
// script or an application makes against the current server, and every one of them must succeed with the same shapes.
// The server may add members, routes and extensions; it may not remove or rename what that client reads, change a
// status or an error code it matches, or refuse a request it sends. A break that is meant, before 1.0, is accepted with
// `just freeze sdk`, which makes today's client library the new baseline; from 1.0 a break needs a new extension name
// and a major version (docs/api.md, "What is frozen").

var (
	prevOnce sync.Once
	prevDir  string
	prevBin  string
	prevErr  error
)

// TestMain removes the program the previous-client test builds once per process.
func TestMain(m *testing.M) {
	code := m.Run()
	if prevDir != "" {
		_ = os.RemoveAll(prevDir)
	}
	os.Exit(code)
}

// prevDriver builds the driver with the baseline library, once per test process.
func prevDriver(t *testing.T) string {
	t.Helper()
	prevOnce.Do(func() {
		goBin, err := exec.LookPath("go")
		if err != nil {
			goBin = filepath.Join(runtime.GOROOT(), "bin", "go")
		}
		if _, err := os.Stat(goBin); err != nil {
			prevErr = err
			return
		}
		prevDir, prevErr = os.MkdirTemp("", "prevsdk")
		if prevErr != nil {
			return
		}
		prevBin = filepath.Join(prevDir, "driver")
		cmd := exec.Command(goBin, "build", "-o", prevBin, ".")
		cmd.Dir = filepath.Join("testdata", "prevsdk_driver")
		// a module of its own with the baseline library next to it: no workspace, no network
		cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod", "GOPROXY=off")
		if out, err := cmd.CombinedOutput(); err != nil {
			prevErr = &buildError{err: err, out: string(out)}
		}
	})
	if prevErr != nil {
		if os.IsNotExist(prevErr) {
			t.Skipf("no go tool to build the previous client: %v", prevErr)
		}
		t.Fatalf("building the previous client: %v", prevErr)
	}
	return prevBin
}

type buildError struct {
	err error
	out string
}

func (b *buildError) Error() string { return b.err.Error() + "\n" + b.out }

// the steps the driver runs: a step that disappears from the driver would otherwise make the test weaker unseen
var prevSteps = []string{
	"info",
	"jail create and wait",
	"jail get with its etag",
	"jail list",
	"jail patch with the current etag",
	"jail patch with a stale etag is a conflict",
	"jail patch with an unknown member is invalid",
	"jail get of a missing jail is not found",
	"jail start, state, stop",
	"snapshot create, list, delete",
	"operations list and get",
	"host objects",
	"firewall policy and address set",
	"jail reach",
	"jail delete and wait",
}

func TestPreviousSDKWorksAgainstTheServer(t *testing.T) {
	bin := prevDriver(t)
	e := dtest.New(t, dtest.Options{})
	e.Seed()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, e.HTTP.URL, e.Token(api.PermAdmin), dtest.SeedProject).CombinedOutput()
	var ok []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if name, found := strings.CutPrefix(l, "ok "); found {
			ok = append(ok, name)
		}
	}
	if err != nil || strings.Join(ok, "\n") != strings.Join(prevSteps, "\n") {
		t.Fatalf("the client of the baseline release cannot use the server (err %v):\n%s", err, out)
	}
}
