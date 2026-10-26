package scenario

import (
	"os"
	"testing"

	"jailops.net/jailops/prototypes/internal/raftproto/proc"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == proc.ChildArg {
		os.Exit(proc.ChildMain(os.Args[2:]))
	}
	os.Exit(m.Run())
}

func runLib(t *testing.T, lib string, processes bool) {
	exe, _ := os.Executable()
	base := Config{Lib: lib, Processes: processes, Exe: exe, Dir: t.TempDir()}
	res := Run(base, Params{Ops: 100, Parallel: 8}, func(n string) bool {
		return !(testing.Short() && n == "throughput")
	}, func(s string) { t.Log("running", s) })
	for _, r := range res {
		t.Logf("%-9s %-22s ok=%v %v  %s", r.Lib, r.Name, r.OK, r.Dur.Round(1e7), r.Detail)
		if !r.OK {
			t.Errorf("%s/%s failed: %s", r.Lib, r.Name, r.Detail)
		}
	}
}

func TestHashicorpInProcess(t *testing.T) { runLib(t, "hashicorp", false) }
func TestEtcdInProcess(t *testing.T)      { runLib(t, "etcd", false) }

func TestHashicorpProcesses(t *testing.T) {
	if testing.Short() {
		t.Skip("-short")
	}
	runLib(t, "hashicorp", true)
}

func TestEtcdProcesses(t *testing.T) {
	if testing.Short() {
		t.Skip("-short")
	}
	runLib(t, "etcd", true)
}
