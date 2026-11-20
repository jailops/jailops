package cli_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
)

func TestJailNotesFlow(t *testing.T) {
	h := newHarness(t)
	h.seed()
	h.run("", "jail", "create", "web", "--image", "freebsd-15", "--network", "lan", "--storage", "default")
	notes := func() string {
		j, _ := h.srv.JailIn("main", "web")
		return j.Spec.Notes
	}
	run := func(stdin string, args ...string) (string, string) {
		t.Helper()
		out, errs, code := h.run(stdin, args...)
		if code != 0 {
			t.Fatalf("%v: exit %d: %s", args, code, errs)
		}
		return out, errs
	}

	// none: nothing printed
	if out, _ := run("", "jail", "notes", "web"); out != "" {
		t.Errorf("empty notes printed %q", out)
	}
	// set from standard input: line feeds only, no blanks at the end
	run("Serves the shop.\r\n\r\n- owner: ops\n\n", "jail", "notes", "set", "web", "--file", "-")
	if got := notes(); got != "Serves the shop.\n\n- owner: ops" {
		t.Fatalf("set: %q", got)
	}
	if out, _ := run("", "jail", "notes", "web"); out != "Serves the shop.\n\n- owner: ops\n" {
		t.Errorf("print: %q", out)
	}
	if out, _ := run("", "jail", "notes", "web", "-o", "json"); !strings.Contains(out, `"notes": "Serves the shop.\n\n- owner: ops"`) {
		t.Errorf("json: %q", out)
	}
	if out, _ := run("", "jail", "show", "web"); !strings.Contains(out, "Notes:       Serves the shop. (2 more lines: jailops jail notes web)") {
		t.Errorf("show: %q", out)
	}

	// the editor gets the notes alone; saving them unchanged sends nothing
	seen := filepath.Join(h.dir, "seen.md")
	look := writeFile(t, h.dir, "ed-look.sh", "#!/bin/sh\ncp \"$1\" "+seen+"\n", 0o755)
	add := writeFile(t, h.dir, "ed-add.sh", "#!/bin/sh\nprintf -- '- pager: 555\\n' >> \"$1\"\n", 0o755)
	empty := writeFile(t, h.dir, "ed-empty.sh", "#!/bin/sh\n: > \"$1\"\n", 0o755)
	h.env["EDITOR"] = look
	before := len(h.mutations())
	run("", "jail", "notes", "edit", "web")
	if b, _ := os.ReadFile(seen); string(b) != "Serves the shop.\n\n- owner: ops\n" {
		t.Errorf("the editor got %q", b)
	}
	if len(h.mutations()) != before {
		t.Errorf("an unchanged edit wrote: %v", h.mutations()[before:])
	}
	h.env["VISUAL"] = add // VISUAL before EDITOR
	run("", "jail", "notes", "edit", "web")
	if got := notes(); got != "Serves the shop.\n\n- owner: ops\n- pager: 555" {
		t.Errorf("edit: %q", got)
	}
	h.env["VISUAL"] = empty // an empty file removes the notes
	run("", "jail", "notes", "edit", "web")
	if got := notes(); got != "" {
		t.Errorf("empty edit: %q", got)
	}
	run("x", "jail", "notes", "set", "web", "--file", "-")
	run("", "jail", "notes", "clear", "web")
	if got := notes(); got != "" {
		t.Errorf("clear: %q", got)
	}
	// a dry run sends nothing
	before = len(h.mutations())
	run("y", "jail", "notes", "set", "web", "--file", "-", "--dry-run")
	if len(h.mutations()) != before {
		t.Error("a dry run wrote")
	}
}

// A change made while the editor is open wins: the edit is refused and its text kept in the file the error names.
func TestJailNotesEditConflict(t *testing.T) {
	h := newHarness(t)
	h.seed()
	h.run("", "jail", "create", "web", "--image", "freebsd-15", "--network", "lan", "--storage", "default")
	ready, gate := filepath.Join(h.dir, "ready"), filepath.Join(h.dir, "gate")
	for _, p := range []string{ready, gate} {
		if err := syscall.Mkfifo(p, 0o600); err != nil {
			t.Skipf("no fifo: %v", err)
		}
	}
	// the editor writes, then says so and waits for the test to change the jail (fifos block: no polling)
	h.env["EDITOR"] = writeFile(t, h.dir, "ed-wait.sh", "#!/bin/sh\necho mine > \"$1\"\necho > "+ready+"\nread x < "+gate+"\n", 0o755)
	type res struct {
		errs string
		code int
	}
	done := make(chan res, 1)
	go func() {
		_, e, c := h.run("", "jail", "notes", "edit", "web")
		done <- res{e, c}
	}()
	b, _ := os.ReadFile(ready) // returns once the editor has written
	_ = b
	h.srv.Touch("main", "web")
	if err := os.WriteFile(gate, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := <-done
	if r.code != 5 || !strings.HasPrefix(r.errs, "precondition_failed:") || !strings.Contains(r.errs, "someone changed the jail") {
		t.Fatalf("conflict: %d %q", r.code, r.errs)
	}
	m := regexp.MustCompile(`saved in (\S+);`).FindStringSubmatch(r.errs)
	if m == nil {
		t.Fatalf("no file named: %q", r.errs)
	}
	defer os.Remove(m[1])
	if kept, _ := os.ReadFile(m[1]); string(kept) != "mine\n" {
		t.Errorf("the kept file has %q", kept)
	}
	if j, _ := h.srv.JailIn("main", "web"); j.Spec.Notes != "" {
		t.Errorf("the stale edit was written: %q", j.Spec.Notes)
	}
}

func TestNetworkNotes(t *testing.T) {
	h := newHarness(t)
	h.seed()
	if _, errs, code := h.run("Uplink to the shop LAN.\n", "network", "notes", "set", "lan", "--file", "-"); code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	if out, _, _ := h.run("", "network", "notes", "lan"); out != "Uplink to the shop LAN.\n" {
		t.Errorf("print: %q", out)
	}
}
