package cli_test

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
	"jailops.net/jailops/sdk/fake"
)

// complete runs __complete and returns the candidates and the directive.
func (h *harness) complete(args ...string) ([]string, int) {
	h.t.Helper()
	out, _, code := h.run("", append([]string{"__complete"}, args...)...)
	if code != 0 {
		h.t.Fatalf("%v: exit %d", args, code)
	}
	var names []string
	dir := -1
	for _, l := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(l, ":") && dir < 0:
			dir, _ = strconv.Atoi(l[1:])
		case l != "" && !strings.HasPrefix(l, "Completion ended"):
			names = append(names, strings.SplitN(l, "\t", 2)[0])
		}
	}
	return names, dir
}

// Tab in jailops cp reads each word by its own shape: a jail name with a colon, the paths inside a jail after the
// colon (never the files of this machine), nothing for the selector form, the shell's own files for a local word.
func TestCpCompletion(t *testing.T) {
	h := cpHarness(t)
	work := t.TempDir()
	t.Chdir(work)
	for _, f := range []string{"wfile.txt", "other.txt", ".wdot"} {
		if err := os.WriteFile(filepath.Join(work, f), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(work, "wdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	const (
		noFile  = 4
		noSpace = 2
		def     = 0
	)
	for _, c := range []struct {
		args []string
		want []string // exactly these, in this order; nil for none
		dir  int
	}{
		// a bare word: jails with a colon, and the files of the directory
		{[]string{"cp", "w"}, []string{"web:", "web2:", "wdir/", "wfile.txt"}, noFile | noSpace},
		{[]string{"cp", "./x", "d"}, []string{"db:"}, noFile | noSpace},
		{[]string{"cp", "wf"}, nil, def},    // no jail matches: the shell's own completion
		{[]string{"cp", "./w"}, nil, def},   // local
		{[]string{"cp", "/et"}, nil, def},   // local
		{[]string{"cp", "~/x"}, nil, def},   // local
		{[]string{"cp", "sub/w"}, nil, def}, // a slash before any colon: local
		{[]string{"cp", "a:b"}, nil, def},   // a colon without a slash after it: a local name
		{[]string{"jail", "cp", "d"}, []string{"db:"}, noFile | noSpace},
		// after the colon: the jail's own paths
		{[]string{"cp", "web:"}, []string{"web:/"}, noFile | noSpace},
		{[]string{"cp", "ghost:"}, nil, noFile},
		{[]string{"cp", "web:/"}, []string{"web:/etc/", "web:/usr/"}, noFile | noSpace},
		{[]string{"cp", "web:/et"}, []string{"web:/etc/"}, noFile | noSpace},
		{[]string{"cp", "web:/etc/"}, []string{"web:/etc/motd", "web:/etc/rc.conf"}, noFile},
		{[]string{"cp", "./a", "web:/etc/m"}, []string{"web:/etc/motd"}, noFile},
		{[]string{"cp", "web:/usr/local/etc/app/"}, []string{"web:/usr/local/etc/app/app.conf", "web:/usr/local/etc/app/conf.d/"}, noFile | noSpace},
		{[]string{"jail", "cp", "web:/et"}, []string{"web:/etc/"}, noFile | noSpace},
		{[]string{"cp", "web:/nothing/"}, nil, noFile},
		{[]string{"cp", "web:/etc/../"}, nil, noFile},
		{[]string{"cp", "web:/etc//"}, nil, noFile},
		// the selector form has no one jail: nothing, and never the files of this machine
		{[]string{"cp", "./x", "-l", "role=web", ":/e"}, nil, noFile},
		{[]string{"cp", "./x", "--all", ":"}, nil, noFile},
	} {
		got, dir := h.complete(c.args...)
		if strings.Join(got, " ") != strings.Join(c.want, " ") || dir != c.dir {
			t.Errorf("%v: got %v (%d), want %v (%d)", c.args, got, dir, c.want, c.dir)
		}
	}
}

// With no server to ask, a bare word keeps the shell's own file completion, and a word after NAME: offers nothing
// rather than the files of this machine.
func TestCpCompletionWithoutServer(t *testing.T) {
	h := cpHarness(t)
	h.ts.Close()
	for _, c := range []struct {
		word string
		dir  int
	}{{"w", 0}, {"", 0}, {"web:", 4}, {"web:/et", 4}, {":/e", 4}} {
		if got, dir := h.complete("cp", c.word); len(got) != 0 || dir != c.dir {
			t.Errorf("%q: got %v (%d), want nothing (%d)", c.word, got, dir, c.dir)
		}
	}
}

// A daemon without the jail_files_list extension gives nothing after the colon, not an error and not host files.
func TestCpCompletionWithoutExtension(t *testing.T) {
	h := newHarness(t, fake.WithExtensions(slices.DeleteFunc(slices.Clone(api.Extensions), func(e string) bool { return e == "jail_files_list" })...))
	h.seed()
	h.run("", "create", "web", "--image", "freebsd-15", "--network", "lan", "--storage", "default")
	h.srv.SeedJailFile("main", "web", "/etc/motd", []byte("x"), 0o644)
	if got, dir := h.complete("cp", "web:/et"); len(got) != 0 || dir != 4 {
		t.Errorf("got %v (%d), want nothing (4)", got, dir)
	}
}
