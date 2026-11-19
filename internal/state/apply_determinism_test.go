package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"jailops.net/jailops/sdk/api"
)

// Applying a command is a pure function of the stored objects and the command: the proposer supplies the id, the time and
// the uuid, and nothing in the apply reads a clock, a random source or the environment. A replicated log depends on
// that: every replica that applies the same commands in the same order ends with the same files. These tests apply one
// command stream to fresh stores and compare the objects/ trees byte for byte, in the direct (single node) committer
// today, so that a committer in front of Apply later changes nothing in what is written.

const (
	detNetA = "11111111-aaaa-4aaa-8aaa-111111111111"
	detSetB = "22222222-bbbb-4bbb-8bbb-222222222222"
	detPolC = "33333333-cccc-4ccc-8ccc-333333333333"
	detNetD = "44444444-dddd-4ddd-8ddd-444444444444"
)

// detCmd is a put or a delete with the proposer's id and a time that depends on the step number only.
func detCmd(step int, c Command) Command {
	c.Version = CommandVersion
	c.ID = fmt.Sprintf("step-%02d", step)
	c.Time = time.Date(2026, 11, 19, 10, 0, step, 123_000_000*(step%3), time.UTC)
	return c
}

func detSpec(s string) json.RawMessage { return json.RawMessage(s) }

// detStream is the command stream: creates, updates by compare-and-swap, an unconditional update, a rename, a move of
// a project, deletes, and every kind of refusal (already exists, conflict, not found, invalid).
func detStream() []Command {
	steps := []Command{
		{Op: OpPut, Kind: api.KindNetwork, UUID: detNetA, Name: "lan", Scope: api.ScopeFleet, Spec: detSpec(`{"v":1}`)},
		{Op: OpPut, Kind: api.KindAddressSet, UUID: detSetB, Name: "office", Scope: api.ScopeFleet, Project: "shop", Spec: detSpec(`{"m":["192.0.2.0/24"]}`)},
		{Op: OpPut, Kind: api.KindFirewallPolicy, UUID: detPolC, Name: "web", Scope: api.ScopeHost, Project: "shop",
			Labels: map[string]string{"tier": "web", "env": "prod"}, ManagedBy: "hub", Spec: detSpec(`{"rules":[]}`)},
		// create only, the object exists
		{Op: OpPut, Kind: api.KindNetwork, UUID: detNetA, Name: "lan", Scope: api.ScopeFleet, Spec: detSpec(`{"v":9}`)},
		// compare and swap: one wins, a stale generation conflicts, the next generation wins
		{Op: OpPut, Kind: api.KindNetwork, UUID: detNetA, Name: "lan", Scope: api.ScopeFleet, Spec: detSpec(`{"v":2}`), ExpectGeneration: 1},
		{Op: OpPut, Kind: api.KindNetwork, UUID: detNetA, Name: "lan", Scope: api.ScopeFleet, Spec: detSpec(`{"v":3}`), ExpectGeneration: 1},
		{Op: OpPut, Kind: api.KindNetwork, UUID: detNetA, Name: "lan", Scope: api.ScopeFleet, Spec: detSpec(`{"v":3}`), ExpectGeneration: 2},
		// unconditional, then a rename (same uuid, another name)
		{Op: OpPut, Kind: api.KindNetwork, UUID: detNetA, Name: "lan", Scope: api.ScopeFleet, Spec: detSpec(`{"v":4}`), ExpectGeneration: AnyGeneration, Host: "host-a"},
		{Op: OpPut, Kind: api.KindNetwork, UUID: detNetA, Name: "lan2", Scope: api.ScopeFleet, Spec: detSpec(`{"v":5}`), ExpectGeneration: 4},
		// another object may not take the name that is in use
		{Op: OpPut, Kind: api.KindNetwork, UUID: detNetD, Name: "lan2", Scope: api.ScopeFleet, Spec: detSpec(`{"v":6}`)},
		// the same name in another kind is fine
		{Op: OpPut, Kind: api.KindAddressSet, UUID: detNetD, Name: "lan2", Scope: api.ScopeFleet, Spec: detSpec(`{"v":7}`)},
		// a create without a uuid is invalid
		{Op: OpPut, Kind: api.KindNetwork, Name: "nouuid", Scope: api.ScopeFleet, Spec: detSpec(`{}`)},
		// the project of an object changes only with MoveProject
		{Op: OpPut, Kind: api.KindAddressSet, UUID: detSetB, Name: "office", Scope: api.ScopeFleet, Project: "shop2", Spec: detSpec(`{"m":[]}`), ExpectGeneration: 1},
		{Op: OpPut, Kind: api.KindAddressSet, UUID: detSetB, Name: "office", Scope: api.ScopeFleet, Project: "shop2", Spec: detSpec(`{"m":["198.51.100.0/24"]}`), ExpectGeneration: 1, MoveProject: true},
		// the policy gets a label and a host
		{Op: OpPut, Kind: api.KindFirewallPolicy, UUID: detPolC, Name: "web", Scope: api.ScopeHost, Project: "shop", Host: "host-b",
			Labels: map[string]string{"tier": "web"}, Spec: detSpec(`{"rules":[1]}`), ExpectGeneration: 1},
		// deletes: a stale generation conflicts, the right one deletes, a second delete finds nothing, by name too
		{Op: OpDelete, Kind: api.KindAddressSet, UUID: detSetB, ExpectGeneration: 99},
		{Op: OpDelete, Kind: api.KindAddressSet, UUID: detSetB, ExpectGeneration: 2},
		{Op: OpDelete, Kind: api.KindAddressSet, UUID: detSetB},
		{Op: OpDelete, Kind: api.KindFirewallPolicy, Name: "web", Scope: api.ScopeHost, Project: "shop", ExpectGeneration: AnyGeneration},
		// an unknown kind that is valid is stored like any other
		{Op: OpPut, Kind: "Widget", UUID: "55555555-eeee-4eee-8eee-555555555555", Name: "w", Scope: api.ScopeHost, Spec: detSpec(`{"w":true}`)},
	}
	for i := range steps {
		steps[i] = detCmd(i+1, steps[i])
	}
	return steps
}

// detOutcome is what the caller sees of one command, comparable as text.
func detOutcome(r Result, err error) string {
	if err != nil {
		var ae *api.Error
		if errors.As(err, &ae) {
			return fmt.Sprintf("error %s: %s", ae.Code, ae.Message)
		}
		return "error: " + err.Error()
	}
	o := r.Object
	return fmt.Sprintf("ok %s created=%v deleted=%v uuid=%s gen=%d project=%q host=%q created_at=%s updated_at=%s",
		r.CommandID, r.Created, r.Deleted, o.Metadata.UUID, o.Metadata.Generation, o.Metadata.Project, o.Metadata.Host,
		o.Metadata.CreatedAt.Format(time.RFC3339Nano), o.Metadata.UpdatedAt.Format(time.RFC3339Nano))
}

// runDetStream applies the stream to a store in dir. With reopen the store is closed and opened again after every
// command, so what is read back from the files is what the next command works on.
func runDetStream(t *testing.T, dir string, reopen bool) []string {
	t.Helper()
	s, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, c := range detStream() {
		r, err := s.Apply(c)
		out = append(out, detOutcome(r, err))
		if reopen {
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if s, err = Open(dir, Options{}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return out
}

// dumpObjects lists every file and directory under dir/objects with its mode and content.
func dumpObjects(t *testing.T, dir string) map[string]string {
	t.Helper()
	root := filepath.Join(dir, "objects")
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			out[rel+"/"] = fmt.Sprintf("dir %v", info.Mode().Perm())
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[rel] = fmt.Sprintf("file %v\n%s", info.Mode().Perm(), b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func compareTrees(t *testing.T, what string, a, b map[string]string) {
	t.Helper()
	keys := map[string]bool{}
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	var names []string
	for k := range keys {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		x, okA := a[k]
		y, okB := b[k]
		switch {
		case !okA:
			t.Errorf("%s: %s exists only in the second tree", what, k)
		case !okB:
			t.Errorf("%s: %s exists only in the first tree", what, k)
		case x != y:
			t.Errorf("%s: %s differs\n--- first\n%s\n--- second\n%s", what, k, x, y)
		}
	}
}

func TestApplyStreamGivesTheSameTreeOnEveryStore(t *testing.T) {
	dirA, dirB, dirC := t.TempDir(), t.TempDir(), t.TempDir()
	outA := runDetStream(t, dirA, false)
	outB := runDetStream(t, dirB, false)
	outC := runDetStream(t, dirC, true)

	// the stream does what it is meant to: it holds every kind of outcome
	want := map[string]bool{"already_exists": false, "conflict": false, "not_found": false, "invalid": false}
	created, deleted := 0, 0
	for _, o := range outA {
		for code := range want {
			if strings.HasPrefix(o, "error "+code+":") {
				want[code] = true
			}
		}
		if strings.HasPrefix(o, "ok ") {
			if strings.Contains(o, "created=true") {
				created++
			}
			if strings.Contains(o, "deleted=true") {
				deleted++
			}
		}
	}
	for code, seen := range want {
		if !seen {
			t.Errorf("the stream has no %s outcome: it no longer exercises every refusal", code)
		}
	}
	if created < 4 || deleted < 2 {
		t.Errorf("the stream creates %d and deletes %d objects", created, deleted)
	}

	for i := range outA {
		if outA[i] != outB[i] {
			t.Errorf("step %d answers differently on two fresh stores:\n%s\n%s", i+1, outA[i], outB[i])
		}
		if outA[i] != outC[i] {
			t.Errorf("step %d answers differently when the store is reopened after every command:\n%s\n%s", i+1, outA[i], outC[i])
		}
	}
	treeA, treeB, treeC := dumpObjects(t, dirA), dumpObjects(t, dirB), dumpObjects(t, dirC)
	compareTrees(t, "two fresh stores", treeA, treeB)
	compareTrees(t, "a store reopened after every command", treeA, treeC)
	for name := range treeA {
		if filepath.Ext(name) == tempSuffix {
			t.Errorf("a temporary file is left in the tree: %s", name)
		}
	}
	if len(treeA) < 8 {
		t.Errorf("the tree is almost empty (%d entries): the comparison proves little", len(treeA))
	}
}

// The files hold what the command says and nothing the apply invented: the times are the proposer's, in UTC, and the
// generation counts the puts.
func TestAppliedFilesHoldTheTimesOfTheCommands(t *testing.T) {
	dir := t.TempDir()
	runDetStream(t, dir, false)
	b, err := os.ReadFile(filepath.Join(dir, "objects", string(api.KindNetwork), detNetA+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var o Object
	if err := json.Unmarshal(b, &o); err != nil {
		t.Fatal(err)
	}
	cmds := detStream()
	// step 1 created it, step 9 renamed it last
	if !o.Metadata.CreatedAt.Equal(cmds[0].Time) || !o.Metadata.UpdatedAt.Equal(cmds[8].Time) {
		t.Errorf("created %v updated %v, want the times of steps 1 and 9: %v %v", o.Metadata.CreatedAt, o.Metadata.UpdatedAt, cmds[0].Time, cmds[8].Time)
	}
	if o.Metadata.CreatedAt.Location() != time.UTC || o.Metadata.UpdatedAt.Location() != time.UTC {
		t.Errorf("stored times are not in UTC: %v %v", o.Metadata.CreatedAt.Location(), o.Metadata.UpdatedAt.Location())
	}
	if o.Metadata.Generation != 5 || o.Metadata.Name != "lan2" {
		t.Errorf("generation %d name %q, want 5 and lan2", o.Metadata.Generation, o.Metadata.Name)
	}
	if _, err := os.Stat(filepath.Join(dir, "objects", string(api.KindNetwork), detNetA+".json"+previousSuffix)); err != nil {
		t.Errorf("an updated object keeps its previous version: %v", err)
	}
}
