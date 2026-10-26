package harness

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestGuardZFS(t *testing.T) {
	root := RootName("tank", 4)
	good := [][]string{
		{"zfs", "create", "-p", root + "/a"},
		{"zfs", "snapshot", root + "/a@s1"},
		{"zfs", "send", "-i", root + "/a#b1", root + "/a@s2"},
		{"zfs", "recv", "-s", root + "/r"},
		{"zfs", "set", "mountpoint=/jop/x", root + "/a"},
		{"zfs", "clone", "-o", "mountpoint=none", root + "/a@s1", root + "/c"},
		{"zfs", "send", "-t", "1-abc-def"},
		{"zfs", "list", "-H", "-o", "name", "-r", root},
	}
	for _, a := range good {
		if err := GuardZFS("tank", root, a); err != nil {
			t.Errorf("%v: %v", a, err)
		}
	}
	bad := [][]string{
		{"zfs", "destroy", "-r", "tank"},
		{"zfs", "destroy", "tank/other"},
		{"zfs", "destroy", "tank/jailops-proto-40/x"},
		{"zfs", "clone", root + "/a@s", "tank/zroot"},
		{"zfs", "set", "origin=tank/x", root + "/a"},
		{"zfs", "destroy", root + "/a@s1,tank/o@s"},
	}
	for _, a := range bad {
		if err := GuardZFS("tank", root, a); err == nil {
			t.Errorf("%v: expected refusal", a)
		}
	}
	if GuardZFS("", "", []string{"zfs", "list"}) == nil {
		t.Error("empty pool must refuse")
	}
}

func TestIfName(t *testing.T) {
	for s, want := range map[string]bool{"jop0": true, "jopa_b": true, "bridge0": false, "jop": false, "jopABC": false, "jop0123456789abc": false} {
		if ValidIfName(s) != want {
			t.Errorf("%s", s)
		}
	}
}

func TestMainRefusesWithoutYes(t *testing.T) {
	var b bytes.Buffer
	s := Spec{N: 4, Name: "x", Run: func(e *Env) { t.Fatal("must not run") }}
	// On linux this refuses on GOOS before the flag check; either way it must not run.
	if rc := Main(s, []string{"--pool", "tank"}, &b); rc == 0 {
		t.Fatalf("rc=0\n%s", b.String())
	}
}

func TestDryRunVerdict(t *testing.T) {
	var b bytes.Buffer
	s := Spec{N: 4, Name: "x", Plan: []string{"p"}, Run: func(e *Env) {
		e.Zfs("create", e.Root+"/a")
		e.Check("c", true, "d")
		out := e.Run("echo", "a b").Out()
		_ = out
	}}
	if rc := Main(s, []string{"--pool", "tank", "--dry-run"}, &b); rc != 0 {
		t.Fatalf("rc=%d\n%s", rc, b.String())
	}
	o := b.String()
	if !strings.Contains(o, "DRY-RUN would run: zfs create tank/jailops-proto-4/a") {
		t.Fatal(o)
	}
	i := strings.LastIndex(strings.TrimSpace(o), "\n")
	var v Verdict
	if err := json.Unmarshal([]byte(strings.TrimSpace(o[i:])), &v); err != nil {
		t.Fatal(err)
	}
	if v.Pass || !v.DryRun || v.Prototype != 4 {
		t.Fatalf("%+v", v)
	}
}

func TestGuardAbortsInRun(t *testing.T) {
	var b bytes.Buffer
	s := Spec{N: 4, Name: "x", Run: func(e *Env) { e.Zfs("destroy", "tank/prod") }}
	Main(s, []string{"--pool", "tank", "--dry-run"}, &b)
	if !strings.Contains(b.String(), "refusing zfs argument") {
		t.Fatal(b.String())
	}
}
