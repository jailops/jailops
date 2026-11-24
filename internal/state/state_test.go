package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"jailops.net/jailops/sdk/api"
)

var t0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func put(kind api.Kind, name, uuid string, expect int64, spec string) Command {
	return Command{Version: CommandVersion, ID: "c-" + name, Time: t0, Op: OpPut, Kind: kind, UUID: uuid, Name: name,
		Scope: api.ScopeFleet, Spec: json.RawMessage(spec), ExpectGeneration: expect}
}

func del(kind api.Kind, uuid string, expect int64) Command {
	return Command{Version: CommandVersion, ID: "d-" + uuid, Time: t0, Op: OpDelete, Kind: kind, UUID: uuid, ExpectGeneration: expect}
}

func open(t *testing.T, dir string, o Options) *Store {
	t.Helper()
	s, err := Open(dir, o)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func mustApply(t *testing.T, s *Store, c Command) Result {
	t.Helper()
	r, err := s.Apply(c)
	if err != nil {
		t.Fatalf("Apply %v: %v", c.String(), err)
	}
	return r
}

func TestPutGetGeneration(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir, Options{})
	u := NewUUID()
	r := mustApply(t, s, put(api.KindNetwork, "lan", u, 0, `{"a":1}`))
	if !r.Created || r.Object.Metadata.Generation != 1 {
		t.Fatalf("create: %+v", r)
	}
	r = mustApply(t, s, put(api.KindNetwork, "lan", u, 1, `{"a":2}`))
	if r.Created || r.Object.Metadata.Generation != 2 {
		t.Fatalf("update: %+v", r)
	}
	o, err := s.Get(api.KindNetwork, u)
	if err != nil || string(o.Spec) != `{"a":2}` || o.APIVersion != api.APIVersion {
		t.Fatalf("get: %v %+v", err, o)
	}
	// file on disk is the api.Document envelope
	b, err := os.ReadFile(filepath.Join(dir, "objects", "Network", u+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc api.Document
	if err := json.Unmarshal(b, &doc); err != nil || doc.Kind != api.KindNetwork || doc.Metadata.Generation != 2 {
		t.Fatalf("envelope: %v %+v", err, doc)
	}
	if _, err := os.Stat(filepath.Join(dir, "objects", "Network", u+".json.previous")); err != nil {
		t.Fatalf("previous missing: %v", err)
	}
}

func TestCASAndErrors(t *testing.T) {
	s := open(t, t.TempDir(), Options{})
	u := NewUUID()
	mustApply(t, s, put(api.KindNetwork, "lan", u, 0, `{}`))
	mustApply(t, s, put(api.KindNetwork, "lan", u, 1, `{"x":1}`))
	other := NewUUID()
	tests := []struct {
		name string
		cmd  Command
		code api.Code
	}{
		{"stale generation", put(api.KindNetwork, "lan", u, 1, `{}`), api.CodePreconditionFailed},
		{"future generation", put(api.KindNetwork, "lan", u, 9, `{}`), api.CodePreconditionFailed},
		{"create existing", put(api.KindNetwork, "lan", u, 0, `{}`), api.CodeAlreadyExists},
		{"create dup name new uuid", put(api.KindNetwork, "lan", other, 0, `{}`), api.CodeAlreadyExists},
		{"update missing", put(api.KindNetwork, "nope", other, 3, `{}`), api.CodePreconditionFailed},
		{"no uuid on create", put(api.KindNetwork, "fresh", "", 0, `{}`), api.CodeInvalid},
		{"bad json spec", put(api.KindNetwork, "x", other, 0, `{`), api.CodeInvalid},
		{"bad name", put(api.KindNetwork, "a/b", other, 0, `{}`), api.CodeInvalid},
		{"bad kind", put("../x", "a", other, 0, `{}`), api.CodeInvalid},
		{"bad uuid", put(api.KindNetwork, "a", "../../etc", 0, `{}`), api.CodeInvalid},
		{"bad version", func() Command { c := put(api.KindNetwork, "a", other, 0, `{}`); c.Version = 99; return c }(), api.CodeVersionSkew},
		{"delete stale", del(api.KindNetwork, u, 1), api.CodePreconditionFailed},
		{"delete missing", del(api.KindNetwork, other, AnyGeneration), api.CodeNotFound},
		{"no id", func() Command { c := put(api.KindNetwork, "a", other, 0, `{}`); c.ID = ""; return c }(), api.CodeInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.Apply(tc.cmd)
			if !api.IsCode(err, tc.code) {
				t.Fatalf("got %v, want %s", err, tc.code)
			}
		})
	}
	// Unconditional put works.
	mustApply(t, s, put(api.KindNetwork, "lan", u, AnyGeneration, `{"y":1}`))
	if o, _ := s.Get(api.KindNetwork, u); o.Metadata.Generation != 3 {
		t.Fatalf("generation %d", o.Metadata.Generation)
	}
}

func TestNameUniquenessPerKindAndScope(t *testing.T) {
	s := open(t, t.TempDir(), Options{})
	mustApply(t, s, put(api.KindNetwork, "x", NewUUID(), 0, `{}`))
	mustApply(t, s, put(api.KindFirewallPolicy, "x", NewUUID(), 0, `{}`)) // other kind
	c := put(api.KindNetwork, "x", NewUUID(), 0, `{}`)
	c.Scope = api.ScopeHost // other scope
	mustApply(t, s, c)
	if _, err := s.GetByName(api.KindNetwork, api.ScopeFleet, "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetByName(api.KindNetwork, api.ScopeHost, "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetByName(api.KindNetwork, "", "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetByName(api.KindNetwork, "", "nope"); !api.IsCode(err, api.CodeNotFound) {
		t.Fatalf("%v", err)
	}
}

func TestRenameAndNameLookupUpdate(t *testing.T) {
	s := open(t, t.TempDir(), Options{})
	u := NewUUID()
	mustApply(t, s, put(api.KindJailTemplate, "old", u, 0, `{}`))
	mustApply(t, s, put(api.KindJailTemplate, "new", u, 1, `{}`))
	if _, err := s.GetByName(api.KindJailTemplate, "", "old"); !api.IsCode(err, api.CodeNotFound) {
		t.Fatalf("old name still resolves: %v", err)
	}
	o, err := s.GetByName(api.KindJailTemplate, "", "new")
	if err != nil || o.Metadata.UUID != u {
		t.Fatal(err)
	}
	// update by name only (no uuid)
	c := put(api.KindJailTemplate, "new", "", 2, `{"v":2}`)
	mustApply(t, s, c)
	if o, _ := s.Get(api.KindJailTemplate, u); o.Metadata.Generation != 3 {
		t.Fatalf("gen %d", o.Metadata.Generation)
	}
}

func TestListSelectors(t *testing.T) {
	s := open(t, t.TempDir(), Options{})
	mk := func(name string, labels map[string]string) {
		c := put(api.KindBackupPolicy, name, NewUUID(), 0, `{}`)
		c.Labels = labels
		mustApply(t, s, c)
	}
	mk("c", map[string]string{"env": "prod", "tier": "db"})
	mk("a", map[string]string{"env": "prod", "tier": "web"})
	mk("b", map[string]string{"env": "dev"})
	mk("d", nil)
	tests := []struct {
		sel  string
		want string
	}{
		{"", "a,b,c,d"},
		{"env=prod", "a,c"},
		{"env==prod", "a,c"},
		{"env!=prod", "b,d"},
		{"tier", "a,c"},
		{"!tier", "b,d"},
		{"env=prod,tier=db", "c"},
		{"env in (dev,prod)", "a,b,c"},
		{"env notin (dev)", "a,c,d"},
		{"env in (dev,prod),tier!=web", "b,c"},
		{"missing=x", ""},
	}
	for _, tc := range tests {
		got, err := s.List(api.KindBackupPolicy, tc.sel)
		if err != nil {
			t.Fatalf("%q: %v", tc.sel, err)
		}
		var names []string
		for _, o := range got {
			names = append(names, o.Metadata.Name)
		}
		if strings.Join(names, ",") != tc.want {
			t.Errorf("%q: got %v want %s", tc.sel, names, tc.want)
		}
	}
	for _, bad := range []string{"a=b,,c", "env in dev", "=x", "a b=c", "env in (a"} {
		if _, err := s.List(api.KindBackupPolicy, bad); !api.IsCode(err, api.CodeInvalid) {
			t.Errorf("selector %q: want invalid, got %v", bad, err)
		}
	}
}

func TestReturnedObjectsAreCopies(t *testing.T) {
	s := open(t, t.TempDir(), Options{})
	u := NewUUID()
	c := put(api.KindNetwork, "n", u, 0, `{"k":1}`)
	c.Labels = map[string]string{"a": "b"}
	r := mustApply(t, s, c)
	r.Object.Metadata.Labels["a"] = "mutated"
	r.Object.Spec[2] = 'X'
	o, _ := s.Get(api.KindNetwork, u)
	if o.Metadata.Labels["a"] != "b" || string(o.Spec) != `{"k":1}` {
		t.Fatalf("store aliased: %+v %s", o.Metadata.Labels, o.Spec)
	}
}

func TestReopenPersists(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	u := NewUUID()
	mustApply(t, s, put(api.KindNetwork, "n", u, 0, `{"k":1}`))
	s.Close()
	s2 := open(t, dir, Options{})
	o, err := s2.Get(api.KindNetwork, u)
	if err != nil || o.Metadata.Name != "n" || o.Metadata.CreatedAt != t0 {
		t.Fatalf("%v %+v", err, o)
	}
	if len(s2.Recoveries()) != 0 {
		t.Fatalf("unexpected recoveries %v", s2.Recoveries())
	}
}

func TestSingleWriterLock(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir, Options{})
	if _, err := Open(dir, Options{}); !api.IsCode(err, api.CodeBusy) {
		t.Fatalf("second open: %v", err)
	}
	s.Close()
	s2, err := Open(dir, Options{})
	if err != nil {
		t.Fatalf("reopen after close: %v", err)
	}
	s2.Close()
}

func TestDeleteAndHook(t *testing.T) {
	var calls []string
	var hookErr error
	s := open(t, t.TempDir(), Options{Hooks: Hooks{BeforeDestructive: func(k api.Kind, n string) error {
		calls = append(calls, fmt.Sprintf("%s/%s", k, n))
		return hookErr
	}}})
	u := NewUUID()
	mustApply(t, s, put(api.KindNetwork, "n", u, 0, `{}`))
	if len(calls) != 0 {
		t.Fatalf("hook ran for put: %v", calls)
	}
	hookErr = errors.New("snapshot failed")
	if _, err := s.Apply(del(api.KindNetwork, u, 1)); err == nil || !strings.Contains(err.Error(), "snapshot failed") {
		t.Fatalf("hook error not propagated: %v", err)
	}
	if _, err := s.Get(api.KindNetwork, u); err != nil {
		t.Fatal("object deleted despite hook failure")
	}
	hookErr = nil
	r := mustApply(t, s, del(api.KindNetwork, u, 1))
	if !r.Deleted || r.Object.Metadata.Name != "n" {
		t.Fatalf("%+v", r)
	}
	if got := strings.Join(calls, ","); got != "Network/n,Network/n" {
		t.Fatalf("calls %s", got)
	}
	if _, err := s.Get(api.KindNetwork, u); !api.IsCode(err, api.CodeNotFound) {
		t.Fatal("still present")
	}
	// name is free again
	mustApply(t, s, put(api.KindNetwork, "n", NewUUID(), 0, `{}`))
	// delete by name
	c := Command{Version: CommandVersion, ID: "dn", Time: t0, Op: OpDelete, Kind: api.KindNetwork, Name: "n"}
	mustApply(t, s, c)
}

func TestDeleteRemovesFilesAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir, Options{})
	u := NewUUID()
	mustApply(t, s, put(api.KindNetwork, "n", u, 0, `{}`))
	mustApply(t, s, put(api.KindNetwork, "n", u, 1, `{"v":2}`))
	mustApply(t, s, del(api.KindNetwork, u, 0))
	s.Close()
	ents, _ := os.ReadDir(filepath.Join(dir, "objects", "Network"))
	if len(ents) != 0 {
		t.Fatalf("leftover files: %v", ents)
	}
	s2 := open(t, dir, Options{})
	if l, _ := s2.List(api.KindNetwork, ""); len(l) != 0 {
		t.Fatal("resurrected")
	}
}

func TestWatch(t *testing.T) {
	s := open(t, t.TempDir(), Options{})
	ch, cancel := s.Watch(8)
	u := NewUUID()
	mustApply(t, s, put(api.KindNetwork, "n", u, 0, `{}`))
	mustApply(t, s, put(api.KindNetwork, "n", u, 1, `{}`))
	mustApply(t, s, del(api.KindNetwork, u, 2))
	_, _ = s.Apply(put(api.KindNetwork, "n", u, 7, `{}`)) // failing, no event
	want := []EventType{EventAdded, EventModified, EventDeleted}
	for i, w := range want {
		select {
		case ev := <-ch:
			if ev.Type != w || ev.UUID != u || ev.Kind != api.KindNetwork {
				t.Fatalf("event %d: %+v", i, ev)
			}
		case <-time.After(time.Second):
			t.Fatalf("timeout event %d", i)
		}
	}
	select {
	case ev := <-ch:
		t.Fatalf("unexpected %+v", ev)
	default:
	}
	cancel()
	cancel()
	if _, ok := <-ch; ok {
		t.Fatal("channel not closed")
	}
	// close of store closes watchers
	ch2, _ := s.Watch(1)
	s.Close()
	if _, ok := <-ch2; ok {
		t.Fatal("not closed on store close")
	}
	ch3, _ := s.Watch(1)
	if _, ok := <-ch3; ok {
		t.Fatal("watch after close should be closed")
	}
}

func TestWatchSlowConsumerResync(t *testing.T) {
	s := open(t, t.TempDir(), Options{})
	ch, cancel := s.Watch(1)
	defer cancel()
	for i := range 3 {
		mustApply(t, s, put(api.KindNetwork, fmt.Sprintf("n%d", i), NewUUID(), 0, `{}`))
	}
	<-ch // drain the one buffered
	mustApply(t, s, put(api.KindNetwork, "n9", NewUUID(), 0, `{}`))
	ev := <-ch
	if ev.Type != EventResync {
		t.Fatalf("want resync, got %+v", ev)
	}
}

func objPath(dir string, kind api.Kind, u string) string {
	return filepath.Join(dir, "objects", string(kind), u+".json")
}

func TestCrashPoints(t *testing.T) {
	points := []string{FaultTempWritten, FaultPreviousLinked, FaultRenamed}
	for _, p := range points {
		t.Run(p, func(t *testing.T) {
			dir := t.TempDir()
			crash := false
			s, err := Open(dir, Options{Fault: func(pt string) error {
				if crash && pt == p {
					return errors.New("injected crash")
				}
				return nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			u := NewUUID()
			mustApply(t, s, put(api.KindNetwork, "n", u, 0, `{"v":1}`))
			mustApply(t, s, put(api.KindNetwork, "n", u, 1, `{"v":2}`))
			crash = true
			if _, err := s.Apply(put(api.KindNetwork, "n", u, 2, `{"v":3}`)); err == nil {
				t.Fatal("expected injected failure")
			}
			// the in-memory state must not reflect the failed write
			if o, _ := s.Get(api.KindNetwork, u); o.Metadata.Generation != 2 {
				t.Fatalf("memory advanced to %d", o.Metadata.Generation)
			}
			s.Close()
			s2 := open(t, dir, Options{})
			o, err := s2.Get(api.KindNetwork, u)
			if err != nil {
				t.Fatal(err)
			}
			g := o.Metadata.Generation
			if g != 2 && g != 3 {
				t.Fatalf("recovered generation %d", g)
			}
			if p == FaultRenamed && g != 3 {
				t.Fatalf("rename completed, want gen 3 got %d", g)
			}
			if p != FaultRenamed && g != 2 {
				t.Fatalf("rename not done, want gen 2 got %d", g)
			}
			// leftover temp files were swept
			ents, _ := os.ReadDir(filepath.Dir(objPath(dir, api.KindNetwork, u)))
			for _, e := range ents {
				if strings.HasSuffix(e.Name(), tempSuffix) {
					t.Fatalf("temp file left: %s", e.Name())
				}
			}
			// and the store is writable again
			mustApply(t, s2, put(api.KindNetwork, "n", u, g, `{"v":4}`))
		})
	}
}

func TestCrashDuringCreateLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir, Options{Fault: func(pt string) error {
		if pt == FaultTempWritten {
			return errors.New("boom")
		}
		return nil
	}})
	u := NewUUID()
	if _, err := s.Apply(put(api.KindNetwork, "n", u, 0, `{}`)); err == nil {
		t.Fatal("want error")
	}
	s.Close()
	s2 := open(t, dir, Options{})
	if _, err := s2.Get(api.KindNetwork, u); !api.IsCode(err, api.CodeNotFound) {
		t.Fatalf("%v", err)
	}
	recs := s2.Recoveries()
	if len(recs) != 1 || recs[0].Action != "dropped-temp" {
		t.Fatalf("recoveries: %+v", recs)
	}
}

func TestCrashDuringDelete(t *testing.T) {
	for _, p := range []string{FaultDeletePrevious, FaultDeleteTargetGone} {
		t.Run(p, func(t *testing.T) {
			dir := t.TempDir()
			crash := false
			s, _ := Open(dir, Options{Fault: func(pt string) error {
				if crash && pt == p {
					return errors.New("boom")
				}
				return nil
			}})
			u := NewUUID()
			mustApply(t, s, put(api.KindNetwork, "n", u, 0, `{}`))
			mustApply(t, s, put(api.KindNetwork, "n", u, 1, `{}`))
			crash = true
			if _, err := s.Apply(del(api.KindNetwork, u, 2)); err == nil {
				t.Fatal("want error")
			}
			if _, err := s.Get(api.KindNetwork, u); err != nil {
				t.Fatal("memory dropped object after failed delete")
			}
			s.Close()
			s2 := open(t, dir, Options{})
			_, err := s2.Get(api.KindNetwork, u)
			if p == FaultDeletePrevious && err != nil {
				t.Fatalf("object should survive: %v", err)
			}
			if p == FaultDeleteTargetGone && !api.IsCode(err, api.CodeNotFound) {
				t.Fatalf("object should be gone: %v", err)
			}
		})
	}
}

func writeCorrupt(t *testing.T, path string, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCorruptionFallsBackToPrevious(t *testing.T) {
	for name, corrupt := range map[string]func(t *testing.T, p string){
		"garbage":   func(t *testing.T, p string) { writeCorrupt(t, p, "{{{not json") },
		"truncated": func(t *testing.T, p string) { writeCorrupt(t, p, `{"apiVersion":"jailops/v1","kind":"Netw`) },
		"empty":     func(t *testing.T, p string) { writeCorrupt(t, p, "") },
		"missing":   func(t *testing.T, p string) { os.Remove(p) },
		"wrong identity": func(t *testing.T, p string) {
			writeCorrupt(t, p, `{"apiVersion":"jailops/v1","kind":"Network","metadata":{"name":"z","uuid":"deadbeef-0000"},"spec":{}}`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			s, _ := Open(dir, Options{})
			u := NewUUID()
			mustApply(t, s, put(api.KindNetwork, "n", u, 0, `{"v":1}`))
			mustApply(t, s, put(api.KindNetwork, "n", u, 1, `{"v":2}`))
			s.Close()
			p := objPath(dir, api.KindNetwork, u)
			corrupt(t, p)
			s2 := open(t, dir, Options{})
			o, err := s2.Get(api.KindNetwork, u)
			if err != nil {
				t.Fatal(err)
			}
			if o.Metadata.Generation != 1 || string(o.Spec) != `{"v":1}` {
				t.Fatalf("expected previous (gen 1), got %d %s", o.Metadata.Generation, o.Spec)
			}
			recs := s2.Recoveries()
			if len(recs) != 1 || recs[0].Action != "restored-previous" {
				t.Fatalf("recoveries %+v", recs)
			}
			// the repaired file is valid and the store continues from it
			mustApply(t, s2, put(api.KindNetwork, "n", u, 1, `{"v":5}`))
		})
	}
}

func TestCorruptionWithoutPrevious(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir, Options{})
	u := NewUUID()
	mustApply(t, s, put(api.KindNetwork, "n", u, 0, `{}`))
	s.Close()
	writeCorrupt(t, objPath(dir, api.KindNetwork, u), "garbage")
	if _, err := Open(dir, Options{}); !api.IsCode(err, api.CodeInternal) {
		t.Fatalf("want internal error, got %v", err)
	}
	// lock must be released after failed open
	s2, err := Open(dir, Options{AllowUnrecoverable: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if recs := s2.Recoveries(); len(recs) != 1 || recs[0].Action != "quarantined" {
		t.Fatalf("%+v", recs)
	}
	if _, err := os.Stat(objPath(dir, api.KindNetwork, u) + corruptSuffix); err != nil {
		t.Fatal("not quarantined")
	}
}

func TestDuplicateNamesOnDiskRefused(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir, Options{})
	mustApply(t, s, put(api.KindNetwork, "n", NewUUID(), 0, `{}`))
	s.Close()
	u2 := "11111111-2222-4333-8444-555555555555"
	writeCorrupt(t, objPath(dir, api.KindNetwork, u2),
		`{"apiVersion":"jailops/v1","kind":"Network","metadata":{"name":"n","uuid":"`+u2+`","scope":"fleet"},"spec":{}}`)
	if _, err := Open(dir, Options{}); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("%v", err)
	}
}

func TestMigrations(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir, Options{})
	u := NewUUID()
	mustApply(t, s, put(api.KindNetwork, "n", u, 0, `{}`))
	s.Close()
	p := objPath(dir, api.KindNetwork, u)

	t.Run("newer refused", func(t *testing.T) {
		b, _ := os.ReadFile(p)
		os.WriteFile(p, []byte(strings.Replace(string(b), api.APIVersion, "jailops/v99", 1)), 0o600)
		if _, err := Open(dir, Options{}); !api.IsCode(err, api.CodeVersionSkew) {
			t.Fatalf("got %v", err)
		}
		// must not fall back to .previous or modify the newer file
		after, _ := os.ReadFile(p)
		if !strings.Contains(string(after), "jailops/v99") {
			t.Fatal("newer file was rewritten")
		}
	})
	t.Run("garbage version refused", func(t *testing.T) {
		b, _ := os.ReadFile(p)
		os.WriteFile(p, []byte(strings.Replace(string(b), "jailops/v99", "other/x", 1)), 0o600)
		if _, err := Open(dir, Options{AllowUnrecoverable: false}); err == nil {
			t.Fatal("want error")
		}
	})
}

func TestMigrateChain(t *testing.T) {
	chain := []Migration{
		{From: "jailops/v1", To: "jailops/v2", Fn: func(d map[string]any) error {
			spec := d["spec"].(map[string]any)
			spec["b"] = spec["a"]
			delete(spec, "a")
			return nil
		}},
		{From: "jailops/v2", To: "jailops/v3", Fn: func(d map[string]any) error { d["spec"].(map[string]any)["c"] = true; return nil }},
	}
	doc := map[string]any{"apiVersion": "jailops/v1", "spec": map[string]any{"a": 1}}
	changed, err := migrateTo(doc, chain, "jailops/v3")
	if err != nil || !changed || doc["apiVersion"] != "jailops/v3" {
		t.Fatalf("%v %v %v", changed, err, doc)
	}
	spec := doc["spec"].(map[string]any)
	if spec["b"] != 1 || spec["c"] != true || spec["a"] != nil {
		t.Fatalf("%v", spec)
	}
	if ch, err := migrateTo(map[string]any{"apiVersion": "jailops/v3"}, chain, "jailops/v3"); ch || err != nil {
		t.Fatal("current must be a no-op")
	}
	if _, err := migrateTo(map[string]any{"apiVersion": "jailops/v4"}, chain, "jailops/v3"); !api.IsCode(err, api.CodeVersionSkew) {
		t.Fatalf("newer not refused: %v", err)
	}
	if _, err := migrateTo(map[string]any{"apiVersion": "jailops/v1"}, chain[1:], "jailops/v3"); !api.IsCode(err, api.CodeInvalid) {
		t.Fatalf("missing step: %v", err)
	}
	if _, err := migrateTo(map[string]any{"apiVersion": "weird"}, chain, "jailops/v3"); !api.IsCode(err, api.CodeInvalid) {
		t.Fatalf("garbage: %v", err)
	}
	failing := []Migration{{From: "jailops/v1", To: "jailops/v2", Fn: func(map[string]any) error { return errors.New("bad") }}}
	if _, err := migrateTo(map[string]any{"apiVersion": "jailops/v1"}, failing, "jailops/v2"); !api.IsCode(err, api.CodeInternal) {
		t.Fatalf("%v", err)
	}
}

func TestCommitterIsPluggable(t *testing.T) {
	var seen []Command
	s := open(t, t.TempDir(), Options{})
	s.SetCommitter(recorder{s: s, seen: &seen})
	mustApply(t, s, put(api.KindNetwork, "n", NewUUID(), 0, `{}`))
	if len(seen) != 1 {
		t.Fatalf("committer not used: %d", len(seen))
	}
	// a replicated command round-trips through JSON and applies identically
	b, _ := seen[0].Marshal()
	c, err := UnmarshalCommand(b)
	if err != nil {
		t.Fatal(err)
	}
	s2 := open(t, t.TempDir(), Options{})
	r1, err := s2.Apply(c)
	if err != nil {
		t.Fatal(err)
	}
	s3 := open(t, t.TempDir(), Options{})
	r2, _ := s3.Apply(c)
	b1, _ := json.Marshal(r1.Object)
	b2, _ := json.Marshal(r2.Object)
	if string(b1) != string(b2) {
		t.Fatalf("not deterministic:\n%s\n%s", b1, b2)
	}
	if _, err := UnmarshalCommand([]byte(`{"version":1`)); err == nil {
		t.Fatal("want decode error")
	}
}

type recorder struct {
	s    *Store
	seen *[]Command
}

func (r recorder) Commit(c Command) (Result, error) {
	*r.seen = append(*r.seen, c)
	return r.s.ApplyCommitted(c)
}

func TestConcurrentWriters(t *testing.T) {
	s := open(t, t.TempDir(), Options{})
	const workers, per = 8, 25
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range per {
				name := fmt.Sprintf("w%d-%d", w, i)
				u := NewUUID()
				if _, err := s.Apply(put(api.KindNetwork, name, u, 0, `{}`)); err != nil {
					t.Error(err)
					return
				}
				s.List(api.KindNetwork, "")
				s.Get(api.KindNetwork, u)
			}
		}()
	}
	wg.Wait()
	l, _ := s.List(api.KindNetwork, "")
	if len(l) != workers*per {
		t.Fatalf("got %d objects", len(l))
	}
}

func TestConcurrentCASExactlyOneWins(t *testing.T) {
	s := open(t, t.TempDir(), Options{})
	u := NewUUID()
	mustApply(t, s, put(api.KindNetwork, "n", u, 0, `{}`))
	const n = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins, conflicts := 0, 0
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := put(api.KindNetwork, "n", u, 1, fmt.Sprintf(`{"i":%d}`, i))
			c.ID = fmt.Sprintf("cas-%d", i)
			_, err := s.Apply(c)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins++
			case api.IsCode(err, api.CodePreconditionFailed):
				conflicts++
			default:
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if wins != 1 || conflicts != n-1 {
		t.Fatalf("wins=%d conflicts=%d", wins, conflicts)
	}
}

func TestConcurrentWatchAndClose(t *testing.T) {
	s := open(t, t.TempDir(), Options{})
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch, cancel := s.Watch(2)
			for range ch {
			}
			cancel()
		}()
	}
	for i := range 20 {
		s.Apply(put(api.KindNetwork, fmt.Sprintf("n%d", i), NewUUID(), 0, `{}`))
	}
	s.Close()
	wg.Wait()
	if _, err := s.Apply(put(api.KindNetwork, "late", NewUUID(), 0, `{}`)); err == nil {
		t.Fatal("apply after close succeeded")
	}
}

func TestNewUUID(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		u := NewUUID()
		if !uuidRe.MatchString(u) || len(u) != 36 || u[14] != '4' || seen[u] {
			t.Fatalf("bad uuid %s", u)
		}
		seen[u] = true
	}
}

func putIn(project string, kind api.Kind, name, uuid string, expect int64, spec string) Command {
	c := put(kind, name, uuid, expect, spec)
	c.Project = project
	return c
}

func TestProjectNameUniqueness(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir, Options{})
	ch, cancel := s.Watch(16)
	defer cancel()
	ua, ub := NewUUID(), NewUUID()
	mustApply(t, s, putIn("a", api.KindFirewallPolicy, "p", ua, 0, `{}`))
	mustApply(t, s, putIn("b", api.KindFirewallPolicy, "p", ub, 0, `{}`))
	if _, err := s.Apply(putIn("a", api.KindFirewallPolicy, "p", NewUUID(), 0, `{}`)); !api.IsCode(err, api.CodeAlreadyExists) {
		t.Fatalf("same project: %v", err)
	}
	// rename within a project, and onto a taken name
	r := mustApply(t, s, putIn("a", api.KindFirewallPolicy, "q", ua, 1, `{}`))
	if r.Object.Metadata.Name != "q" || r.Object.Metadata.Project != "a" {
		t.Fatalf("%+v", r.Object.Metadata)
	}
	mustApply(t, s, putIn("a", api.KindFirewallPolicy, "p", NewUUID(), 0, `{}`))
	if _, err := s.Apply(putIn("a", api.KindFirewallPolicy, "p", ua, 2, `{}`)); !api.IsCode(err, api.CodeAlreadyExists) {
		t.Fatalf("rename onto taken: %v", err)
	}
	// project immutability
	if _, err := s.Apply(putIn("b", api.KindFirewallPolicy, "q", ua, 2, `{}`)); !api.IsCode(err, api.CodeInvalid) {
		t.Fatalf("project change: %v", err)
	}
	if _, err := s.Apply(put(api.KindFirewallPolicy, "q", ua, 2, `{}`)); !api.IsCode(err, api.CodeInvalid) {
		t.Fatalf("project dropped: %v", err)
	}
	if _, err := s.GetByNameIn(api.KindFirewallPolicy, api.ScopeFleet, "b", "q"); !api.IsCode(err, api.CodeNotFound) {
		t.Fatalf("%v", err)
	}
	o, err := s.GetByNameIn(api.KindFirewallPolicy, "", "b", "p")
	if err != nil || o.Metadata.UUID != ub {
		t.Fatalf("%v %v", o, err)
	}
	if _, err := s.GetByName(api.KindFirewallPolicy, "", "p"); err == nil {
		t.Fatal("GetByName must only see project-less objects")
	}
	l, _ := s.ListIn(api.KindFirewallPolicy, "a", "")
	if len(l) != 2 || l[0].Metadata.Name != "p" || l[1].Metadata.Name != "q" {
		t.Fatalf("%v", l)
	}
	if all, _ := s.List(api.KindFirewallPolicy, ""); len(all) != 3 {
		t.Fatalf("%d", len(all))
	}
	ev := <-ch
	if ev.Project != "a" || ev.Type != EventAdded {
		t.Fatalf("%+v", ev)
	}
	// delete resolves by project
	d := Command{Version: CommandVersion, ID: "d1", Time: t0, Op: OpDelete, Kind: api.KindFirewallPolicy, Name: "p", Scope: api.ScopeFleet, Project: "b"}
	if r := mustApply(t, s, d); r.Object.Metadata.UUID != ub {
		t.Fatalf("%+v", r.Object.Metadata)
	}
	// reload keeps the index
	s.Close()
	s2 := open(t, dir, Options{})
	if _, err := s2.GetByNameIn(api.KindFirewallPolicy, api.ScopeFleet, "a", "q"); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.GetByNameIn(api.KindFirewallPolicy, api.ScopeFleet, "b", "p"); err == nil {
		t.Fatal("deleted object came back")
	}
	if _, err := s2.Apply(putIn("a", api.KindFirewallPolicy, "q", NewUUID(), 0, `{}`)); !api.IsCode(err, api.CodeAlreadyExists) {
		t.Fatalf("%v", err)
	}
}

func TestProjectReplayDeterministic(t *testing.T) {
	ua, ub := "11111111-aaaa-4aaa-8aaa-111111111111", "22222222-aaaa-4aaa-8aaa-222222222222"
	cmds := []Command{
		putIn("a", api.KindAddressSet, "x", ua, 0, `{"n":1}`),
		putIn("b", api.KindAddressSet, "x", ub, 0, `{"n":2}`),
		putIn("a", api.KindAddressSet, "x", ua, 1, `{"n":3}`),
	}
	run := func() []string {
		s := open(t, t.TempDir(), Options{})
		var out []string
		for _, c := range cmds {
			b, err := c.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			rc, err := UnmarshalCommand(b)
			if err != nil {
				t.Fatal(err)
			}
			r := mustApply(t, s, rc)
			j, _ := json.Marshal(r.Object)
			out = append(out, string(j))
		}
		return out
	}
	a, b := run(), run()
	if strings.Join(a, "\n") != strings.Join(b, "\n") {
		t.Fatalf("replay differs:\n%v\n%v", a, b)
	}
}

// A tunnel stored by 0.8.3 as `down` must not come up after the upgrade: the document is rewritten with `disabled` when
// it is loaded, and a document that has `disabled` already keeps it.
func TestStoredWireGuardDownBecomesDisabled(t *testing.T) {
	doc := map[string]any{"kind": "WireGuard", "spec": map[string]any{"interface": "wg-office", "down": true}}
	if !renameFields(doc) {
		t.Fatal("a stored `down` was not rewritten")
	}
	spec := doc["spec"].(map[string]any)
	if spec["disabled"] != true || spec["down"] != nil {
		t.Fatalf("spec after the rewrite: %v", spec)
	}
	if renameFields(doc) {
		t.Fatal("the rewrite is not idempotent")
	}
	both := map[string]any{"kind": "WireGuard", "spec": map[string]any{"down": false, "disabled": true}}
	renameFields(both)
	if both["spec"].(map[string]any)["disabled"] != true {
		t.Fatalf("an existing `disabled` was overwritten: %v", both["spec"])
	}
	other := map[string]any{"kind": "Network", "spec": map[string]any{"down": true}}
	if renameFields(other) {
		t.Fatal("another kind was touched")
	}
	raw := []byte(`{"apiVersion":"jailops/v1","kind":"WireGuard","metadata":{"uuid":"u1","name":"office"},"spec":{"interface":"wg-office","down":true}}`)
	o, changed, err := decodeObject(raw, nil)
	if err != nil || !changed {
		t.Fatalf("decode: changed=%v err=%v", changed, err)
	}
	var spec2 struct {
		Disabled bool `json:"disabled"`
		Down     bool `json:"down"`
	}
	if err := json.Unmarshal(o.Spec, &spec2); err != nil || !spec2.Disabled || spec2.Down {
		t.Fatalf("decoded spec %s: %v", o.Spec, err)
	}
}

// A redirect rule of a policy and the proxy keys of a profile are taken out when the object is loaded, and handed over as
// forwards for the service to give to the jails that used them.
func TestForwardsAreTakenOutOfPoliciesAndProfiles(t *testing.T) {
	policy := []byte(`{"apiVersion":"jailops/v1","kind":"FirewallPolicy","metadata":{"uuid":"u1","name":"web","project":"alpha"},"spec":{"default_in":"deny","default_out":"allow","rules":[` +
		`{"name":"ssh","action":"allow","direction":"in","proto":"tcp","ports":["22"]},` +
		`{"name":"fwd","comment":"site","action":"allow","direction":"in","proto":"tcp","from":["192.0.2.0/24"],"redirect":{"listen":"192.0.2.7","host_port":8080,"jail_port":80}},` +
		`{"name":"off","action":"allow","direction":"in","proto":"tcp","disabled":true,"redirect":{"host_port":9090,"jail_port":90}}]}}`)
	o, changed, fw, _, err := decodeObjectForwards(policy, nil)
	if err != nil || !changed {
		t.Fatalf("decode: changed=%v err=%v", changed, err)
	}
	if len(fw) != 2 || !fw[1].Disabled || fw[1].HostPort != 9090 || fw[0].Disabled || fw[0].Policy != "web" || fw[0].Project != "alpha" || fw[0].Rule != 2 || fw[0].HostPort != 8080 || fw[0].JailPort != 80 ||
		fw[0].Listen != "192.0.2.7" || fw[0].Proto != "tcp" || len(fw[0].From) != 1 || fw[0].Comment != "site" {
		t.Fatalf("forwards: %+v", fw)
	}
	if strings.Contains(string(o.Spec), "redirect") || strings.Count(string(o.Spec), `"name"`) != 1 {
		t.Fatalf("the policy keeps the forwards: %s", o.Spec)
	}
	// loaded again, nothing is left to take
	b, err := encodeObject(o)
	if err != nil {
		t.Fatal(err)
	}
	if _, changed, fw, _, err = decodeObjectForwards(b, nil); err != nil || changed || len(fw) != 0 {
		t.Fatalf("second load: changed=%v forwards=%v err=%v", changed, fw, err)
	}

	profile := []byte(`{"apiVersion":"jailops/v1","kind":"Profile","metadata":{"uuid":"u2","name":"pub","project":"alpha"},"spec":{"config":{"resources.memory":"1G",` +
		`"proxy.http.listen":":8080","proxy.http.jail_port":"80","proxy.dns.listen":"192.0.2.7:53","proxy.dns.jail_port":"53","proxy.dns.proto":"udp"}}}`)
	o, changed, fw, _, err = decodeObjectForwards(profile, nil)
	if err != nil || !changed || len(fw) != 2 {
		t.Fatalf("profile: changed=%v forwards=%v err=%v", changed, fw, err)
	}
	if fw[0].Device != "dns" || fw[0].Proto != "udp" || fw[0].Listen != "192.0.2.7" || fw[0].HostPort != 53 || fw[0].JailPort != 53 ||
		fw[1].Device != "http" || fw[1].Proto != "tcp" || fw[1].Listen != "" || fw[1].HostPort != 8080 || fw[1].Profile != "pub" {
		t.Fatalf("profile forwards: %+v", fw)
	}
	if strings.Contains(string(o.Spec), "proxy") || !strings.Contains(string(o.Spec), "resources.memory") {
		t.Fatalf("the profile keeps proxy keys or lost the others: %s", o.Spec)
	}
}

// The forwards taken out of a policy at load stay on disk until the service has given them to the jails: a stop between the
// two (the policy is already rewritten) loses none of them.
func TestTakenForwardsSurviveARestartUntilApplied(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "objects", "FirewallPolicy"), 0o755); err != nil {
		t.Fatal(err)
	}
	doc := `{"apiVersion":"jailops/v1","kind":"FirewallPolicy","metadata":{"uuid":"u1","name":"web","project":"alpha"},"spec":{"default_in":"deny","default_out":"allow","rules":[` +
		`{"name":"fwd","action":"allow","direction":"in","proto":"tcp","redirect":{"host_port":8080,"jail_port":80}}]}}`
	if err := os.WriteFile(filepath.Join(dir, "objects", "FirewallPolicy", "u1.json"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	s.Close() // the service never took them
	s, err = Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	fw := s.TakeMigratedForwards()
	if len(fw) != 1 || fw[0].HostPort != 8080 {
		t.Fatalf("the forward is lost after a restart: %+v", fw)
	}
	if err := s.ForwardsApplied(); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if fw := s.TakeMigratedForwards(); len(fw) != 0 {
		t.Fatalf("an applied forward comes back: %+v", fw)
	}
}

// A network stored by 0.8.3 with `ipv6.mode: proxy` keeps its NDP proxy after the upgrade: the document is rewritten
// with `ndp_proxy: true`, `routed` loses the field, and `none` loses the IPv6 block.
func TestStoredIPv6ModeBecomesNDPProxySwitch(t *testing.T) {
	mk := func(mode string) map[string]any {
		return map[string]any{"kind": "Network", "spec": map[string]any{"ipv6": map[string]any{"mode": mode, "subnet": "2001:db8::/64"}}}
	}
	proxy := mk("proxy")
	if !renameFields(proxy) {
		t.Fatal("a stored mode was not rewritten")
	}
	v6 := proxy["spec"].(map[string]any)["ipv6"].(map[string]any)
	if v6["ndp_proxy"] != true || v6["mode"] != nil {
		t.Fatalf("ipv6 after the rewrite: %v", v6)
	}
	if renameFields(proxy) {
		t.Fatal("the rewrite is not idempotent")
	}
	routed := mk("routed")
	renameFields(routed)
	if v6 := routed["spec"].(map[string]any)["ipv6"].(map[string]any); v6["ndp_proxy"] != nil || v6["mode"] != nil {
		t.Fatalf("routed: %v", v6)
	}
	none := mk("none")
	renameFields(none)
	if none["spec"].(map[string]any)["ipv6"] != nil {
		t.Fatalf("none: %v", none["spec"])
	}
}

// A jail that named an image alias, an image that listed aliases, a backup bundle and a project allow-list are rewritten to the
// bare image name when loaded; the jails are handed over so the service can say which alias each had used.
func TestImageAliasesAreStrippedFromStoredObjects(t *testing.T) {
	jail := []byte(`{"apiVersion":"jailops/v1","kind":"Jail","metadata":{"uuid":"u1","name":"web","project":"alpha"},"spec":{"image":"freebsd-15@stable","storage":"default"}}`)
	o, changed, _, refs, err := decodeObjectForwards(jail, nil)
	if err != nil || !changed || len(refs) != 1 || refs[0] != (MigratedImageRef{Project: "alpha", Jail: "web", Image: "freebsd-15", Alias: "stable"}) {
		t.Fatalf("jail: changed=%v refs=%+v err=%v", changed, refs, err)
	}
	if !strings.Contains(string(o.Spec), `"image":"freebsd-15"`) || strings.Contains(string(o.Spec), "@") {
		t.Fatalf("the jail keeps the alias: %s", o.Spec)
	}
	b, err := encodeObject(o)
	if err != nil {
		t.Fatal(err)
	}
	if _, changed, _, refs, err = decodeObjectForwards(b, nil); err != nil || changed || len(refs) != 0 {
		t.Fatalf("second load: changed=%v refs=%v err=%v", changed, refs, err)
	}
	pinned := []byte(`{"apiVersion":"jailops/v1","kind":"Jail","metadata":{"uuid":"u1","name":"web","project":"alpha"},"spec":{"image":"freebsd-15#g3"}}`)
	if _, changed, _, refs, err = decodeObjectForwards(pinned, nil); err != nil || changed || len(refs) != 0 {
		t.Fatalf("a pinned generation is not an alias: changed=%v refs=%v err=%v", changed, refs, err)
	}

	image := []byte(`{"apiVersion":"jailops/v1","kind":"Image","metadata":{"uuid":"u2","name":"freebsd-15"},"spec":{"os":"freebsd","release":"15.1-RELEASE","aliases":["stable"]}}`)
	o, changed, _, refs, err = decodeObjectForwards(image, nil)
	if err != nil || !changed || len(refs) != 0 || strings.Contains(string(o.Spec), "aliases") || !strings.Contains(string(o.Spec), "release") {
		t.Fatalf("image: changed=%v refs=%v spec=%s err=%v", changed, refs, o.Spec, err)
	}

	backup := []byte(`{"apiVersion":"jailops/v1","kind":"Backup","metadata":{"uuid":"u3","name":"b1","project":"alpha"},"spec":{"jail":"web","bundle":{"spec":{"image":"freebsd-15@lts"}}}}`)
	o, changed, _, refs, err = decodeObjectForwards(backup, nil)
	if err != nil || !changed || len(refs) != 1 || refs[0].Alias != "lts" || strings.Contains(string(o.Spec), "@") {
		t.Fatalf("backup: changed=%v refs=%v spec=%s err=%v", changed, refs, o.Spec, err)
	}

	project := []byte(`{"apiVersion":"jailops/v1","kind":"Project","metadata":{"uuid":"u4","name":"alpha"},"spec":{"images":["freebsd-15@stable","alpine-3"]}}`)
	o, changed, _, _, err = decodeObjectForwards(project, nil)
	if err != nil || !changed || strings.Contains(string(o.Spec), "@") || !strings.Contains(string(o.Spec), "alpine-3") {
		t.Fatalf("project: changed=%v spec=%s err=%v", changed, o.Spec, err)
	}
}

// The rewritten jail is written back at load, and the store hands the alias references over once.
func TestStoredAliasReferencesSurviveTheLoad(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "objects", "Jail"), 0o755); err != nil {
		t.Fatal(err)
	}
	doc := `{"apiVersion":"jailops/v1","kind":"Jail","metadata":{"uuid":"u1","name":"web","project":"alpha"},"spec":{"image":"freebsd-15@stable"}}`
	path := filepath.Join(dir, "objects", "Jail", "u1.json")
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	refs := s.TakeMigratedImageRefs()
	if len(refs) != 1 || refs[0].Jail != "web" || refs[0].Alias != "stable" {
		t.Fatalf("refs: %+v", refs)
	}
	if again := s.TakeMigratedImageRefs(); len(again) != 0 {
		t.Fatalf("handed over twice: %+v", again)
	}
	if b, err := os.ReadFile(path); err != nil || strings.Contains(string(b), "@") {
		t.Fatalf("the file keeps the alias: %s %v", b, err)
	}
}

// A jail stored by a release that had spec.provision still loads: the field is not read any more.
func TestStoredJailWithProvisionLoads(t *testing.T) {
	raw := []byte(`{"apiVersion":"jailops/v1","kind":"Jail","metadata":{"uuid":"u1","name":"web"},"spec":{"image":"fb","provision":{"packages":["nginx"]}}}`)
	o, _, err := decodeObject(raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	var spec api.JailSpec
	if err := json.Unmarshal(o.Spec, &spec); err != nil || spec.Image != "fb" {
		t.Fatalf("%+v %v", spec, err)
	}
}
