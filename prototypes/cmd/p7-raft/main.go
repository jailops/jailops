// Prototype 7: Raft library comparison (design section 11, table row 7).
//
// Runs the same eight scenarios against hashicorp/raft (+ raft-boltdb) and go.etcd.io/raft/v3:
// election and replication, crash and restart, leader change, partition, snapshot and
// compaction, quorum loss and recovery, member add and removal, write throughput.
// All state lives in temporary directories; no ZFS, no network outside 127.0.0.1.
//
// Option not built: a CGO SQLite-based Raft library exists and would be admitted only behind
// a build tag, because the default binary stays CGO_ENABLED=0 (design section 11). It is
// listed in the table as "not built, not run" and nothing here claims anything about it.
//
// Linux note: the harness runs real scenarios only on FreeBSD (as root, with the consent flag).
// On Linux use `go test ./internal/raftproto/scenario` which runs the identical scenarios.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"jailops.net/jailops/prototypes/internal/harness"
	"jailops.net/jailops/prototypes/internal/raftproto/etcdraft"
	"jailops.net/jailops/prototypes/internal/raftproto/hashi"
	"jailops.net/jailops/prototypes/internal/raftproto/proc"
	"jailops.net/jailops/prototypes/internal/raftproto/scenario"
)

var (
	libsFlag  string
	mode      string
	ops       int
	parallel  int
	dataDir   string
	only      string
	keep      bool
	dirPrefix = "jop-proto-7-"
)

func parent() string {
	if dataDir != "" {
		return dataDir
	}
	return os.TempDir()
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == proc.ChildArg {
		os.Exit(proc.ChildMain(os.Args[2:]))
	}
	harness.Exit(harness.Spec{
		N:       7,
		Name:    "raft",
		NoPool:  true,
		Summary: "Raft library comparison: hashicorp/raft+raft-boltdb versus etcd raft on a three-node key-value FSM.",
		Plan: []string{
			"create a temporary directory " + dirPrefix + "<pid> (under --data-dir or the system temp dir)",
			"for each library (--libs): start three nodes behind a partition proxy on 127.0.0.1 (in process, or --mode processes: three child processes)",
			"run: election-replication, crash-restart (SIGKILL in processes mode), leader-change, partition, snapshot-compaction, quorum-loss-recovery, member-add-remove, throughput (--ops, fsync on)",
			"print a comparison table and the verdict; remove the temporary directory unless --keep",
		},
		Flags: func(e *harness.Env, fs *flag.FlagSet) {
			fs.StringVar(&libsFlag, "libs", "hashicorp,etcd", "libraries to run")
			fs.StringVar(&mode, "mode", "inproc", "inproc or processes")
			fs.IntVar(&ops, "ops", 300, "tiny commands in the throughput scenario")
			fs.IntVar(&parallel, "parallel", 16, "concurrent writers in the parallel throughput run")
			fs.StringVar(&dataDir, "data-dir", "", "parent directory for node data (default: system temp dir)")
			fs.StringVar(&only, "scenarios", "", "comma separated scenario names (default all)")
			fs.BoolVar(&keep, "keep", false, "keep the data directory")
		},
		Run:     run,
		Cleanup: cleanup,
	})
}

func cleanup(e *harness.Env) {
	m, _ := filepath.Glob(filepath.Join(parent(), dirPrefix+"*"))
	for _, d := range m {
		e.Printf("   removing %s\n", d)
		os.RemoveAll(d)
	}
}

func run(e *harness.Env) {
	libs := strings.Split(libsFlag, ",")
	for _, l := range libs {
		if l != "hashicorp" && l != "etcd" {
			e.Abort("unknown library %q", l)
		}
	}
	if mode != "inproc" && mode != "processes" {
		e.Abort("--mode must be inproc or processes")
	}
	if e.DryRun {
		e.Note("would run libs=%v mode=%s ops=%d parallel=%d", libs, mode, ops, parallel)
		return
	}
	exe, err := os.Executable()
	if err != nil {
		e.Abort("os.Executable: %v", err)
	}
	dir := filepath.Join(parent(), fmt.Sprintf("%s%d", dirPrefix, os.Getpid()))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		e.Abort("%v", err)
	}
	if !keep {
		e.OnCleanup(func() { os.RemoveAll(dir) })
	} else {
		e.Note("data kept in %s", dir)
	}
	var want func(string) bool
	if only != "" {
		set := map[string]bool{}
		for _, s := range strings.Split(only, ",") {
			set[s] = true
		}
		want = func(n string) bool { return set[n] }
	}
	results := map[string][]scenario.Result{}
	for _, lib := range libs {
		e.Step("library %s (%s mode)", lib, mode)
		res := scenario.Run(scenario.Config{Lib: lib, Processes: mode == "processes", Exe: exe, Dir: dir},
			scenario.Params{Ops: ops, Parallel: parallel}, want, func(s string) { e.Note("running %s", s) })
		results[lib] = res
		for _, r := range res {
			e.Check(lib+"/"+r.Name, r.OK, fmt.Sprintf("%s [%s]", r.Detail, r.Dur.Round(1e7)))
		}
	}
	e.Step("comparison")
	e.Printf("%s\n", scenario.Table(libs, results, notes(libs)))
	e.Resolve("#7 Raft library choice: scenario results above for " + strings.Join(libs, ", ") + " (" + mode + " mode, FreeBSD)")
	e.Resolve("#8 (part) bbolt fsync, TCP on 127.0.0.1 and SIGKILL of child processes work on FreeBSD for both libraries (when all checks pass)")
}

func notes(libs []string) []scenario.Note {
	loc := map[string]string{
		"hashicorp": fmt.Sprintf("%d (node.go; store, transport, snapshots from libs)", scenario.CountLines(hashi.SourceFS)),
		"etcd":      fmt.Sprintf("%d (node, bbolt log, TCP transport)", scenario.CountLines(etcdraft.SourceFS)),
	}
	return []scenario.Note{
		{Row: "adapter Go lines (non-test)", Values: loc},
		{Row: "durable log, snapshots, transport", Values: map[string]string{"hashicorp": "provided (boltdb, file snapshots, net transport)", "etcd": "written by the adapter"}},
		{Row: "quorum-loss recovery", Values: map[string]string{"hashicorp": "raft.RecoverCluster (library API, offline)", "etcd": "no library API; adapter appends ConfChange removals and commits them"}},
		{Row: "membership change", Values: map[string]string{"hashicorp": "AddVoter/RemoveServer futures", "etcd": "ProposeConfChange + app calls ApplyConfChange"}},
		{Row: "apply path", Values: map[string]string{"hashicorp": "FSM interface called by library", "etcd": "app loop consumes Ready, app owns ordering"}},
		{Row: "binary size delta", Values: map[string]string{"hashicorp": "not measured", "etcd": "not measured"}},
		{Row: "CGO SQLite-based Raft library", Values: map[string]string{"hashicorp": "not built, not run (needs CGO)", "etcd": "not built, not run (needs CGO)"}},
	}
}
