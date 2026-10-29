package jailos

import (
	"context"
	"strings"
	"testing"

	"jailops.net/jailops/internal/sys/run"
	"jailops.net/jailops/sdk/api"
)

// rctlOut is what `rctl -u jail:alpha_web` prints: every racct counter of
// rctl(8), one per line, in the kernel's order.
const rctlOut = `cputime=297
datasize=5451776
stacksize=270336
coredumpsize=0
memoryuse=407048192
memorylocked=0
maxproc=23
openfiles=140
vmemoryuse=1364320256
pseudoterminals=0
swapuse=0
nthr=28
msgqqueued=0
msgqsize=0
nmsgq=0
nsem=0
nsemop=0
nshm=0
shmsize=0
wallclock=86213
pcpu=1
readbps=0
writebps=0
readiops=0
writeiops=0
`

func TestParseRctlUsage(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want Usage
		bad  bool
	}{
		{"full", rctlOut, Usage{Processes: 23, CPUSeconds: 297, MemoryBytes: 407048192}, false},
		{"fresh jail", "cputime=0\nmemoryuse=0\nmaxproc=1\n", Usage{Processes: 1}, false},
		{"blank lines and no final newline", "\nmaxproc=4\n\ncputime=12", Usage{Processes: 4, CPUSeconds: 12}, false},
		{"only other counters", "wallclock=5\n", Usage{}, false},
		{"empty", "", Usage{}, true},
		{"humanized (-h)", "memoryuse=388M\nmaxproc=23\n", Usage{}, true},
		{"negative", "cputime=-1\n", Usage{}, true},
		{"no equals", "cputime 297\n", Usage{}, true},
		{"no name", "=297\n", Usage{}, true},
		{"error text", "rctl: rctl_get_racct: Function not implemented\n", Usage{}, true},
	}
	for _, c := range cases {
		got, err := ParseRctlUsage(c.in)
		if c.bad {
			if err == nil {
				t.Errorf("%s: accepted %q as %+v", c.name, c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got.Processes != c.want.Processes || got.CPUSeconds != c.want.CPUSeconds || got.MemoryBytes != c.want.MemoryBytes {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
		if n := len(strings.Fields(c.in)); len(got.Counters) != n {
			t.Errorf("%s: %d counters kept of %d", c.name, len(got.Counters), n)
		}
	}
	u, _ := ParseRctlUsage(rctlOut)
	if u.Counters["nthr"] != 28 || u.Counters["wallclock"] != 86213 {
		t.Errorf("other counters: %v", u.Counters)
	}
}

func TestReadUsage(t *testing.T) {
	ctx := context.Background()
	r := run.NewFake()
	r.On("rctl", []string{"-u", "jail:alpha_web"}, rctlOut)
	r.OnFail("rctl", []string{"-u", "jail:alpha_off"}, 1, "rctl: rctl_get_racct: Function not implemented\n")
	u, err := ReadUsage(ctx, r, "alpha_web")
	if err != nil || u.Processes != 23 || u.MemoryBytes != 407048192 {
		t.Fatalf("%+v %v", u, err)
	}
	if last := r.Calls()[len(r.Calls())-1].Cmd; last.Name != "rctl" || last.Timeout == 0 {
		t.Errorf("ran %+v", last)
	}
	// A failing rctl says what it said; the counters are not zeros.
	_, err = ReadUsage(ctx, r, "alpha_off")
	if err == nil || !strings.Contains(err.Error(), "Function not implemented") {
		t.Errorf("rctl failure: %v", err)
	}
	// No command runs for a name jail(8) would not accept.
	n := len(r.Calls())
	if _, err := ReadUsage(ctx, r, "a.b"); !api.IsCode(err, api.CodeInvalid) || len(r.Calls()) != n {
		t.Errorf("bad name: %v", err)
	}
}
