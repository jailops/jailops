package cli_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"jailops.net/jailops/internal/cli"
)

// TestFreezeCLITree freezes the command tree: every command path with its aliases and every flag (name, shorthand, type,
// default). A command or flag that is added, renamed or removed shows up as a diff of testdata/freeze/cli_tree.golden.
// A change that is meant is accepted with `just freeze`.
func TestFreezeCLITree(t *testing.T) {
	root := cli.NewRootCommand(&cli.Env{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, Stdin: strings.NewReader("")})
	var lines []string
	var walk func(c *cobra.Command, path string)
	walk = func(c *cobra.Command, path string) {
		if c.Hidden || c.Name() == "help" || c.Name() == "completion" {
			return
		}
		head := path
		if len(c.Aliases) > 0 {
			al := append([]string(nil), c.Aliases...)
			sort.Strings(al)
			head += "  (aliases: " + strings.Join(al, ", ") + ")"
		}
		lines = append(lines, head)
		var fl []string
		add := func(f *pflag.Flag) {
			if f.Hidden || f.Name == "help" {
				return
			}
			s := "--" + f.Name
			if f.Shorthand != "" {
				s += "/-" + f.Shorthand
			}
			s += " " + f.Value.Type()
			if d := f.DefValue; d != "" && d != "false" && d != "[]" && d != "0" {
				s += " =" + d
			}
			fl = append(fl, s)
		}
		c.LocalFlags().VisitAll(add)
		sort.Strings(fl)
		for _, f := range fl {
			lines = append(lines, "    "+f)
		}
		subs := c.Commands()
		sort.Slice(subs, func(i, j int) bool { return subs[i].Name() < subs[j].Name() })
		for _, s := range subs {
			walk(s, path+" "+s.Name())
		}
	}
	walk(root, root.Name())
	lines = append(lines, "", "exit codes",
		fmt.Sprintf("    %d ok", cli.ExitOK), fmt.Sprintf("    %d failure", cli.ExitFailure),
		fmt.Sprintf("    %d usage", cli.ExitUsage), fmt.Sprintf("    %d auth", cli.ExitAuth),
		fmt.Sprintf("    %d not found", cli.ExitNotFound), fmt.Sprintf("    %d conflict", cli.ExitConflict),
		fmt.Sprintf("    %d unsupported", cli.ExitUnsupported), fmt.Sprintf("    %d internal", cli.ExitInternal),
		fmt.Sprintf("    %d firewall test: undecided", cli.ExitUndecided), fmt.Sprintf("    %d firewall test: blocked", cli.ExitBlocked),
		fmt.Sprintf("    %d exec: jailops failed, the command did not run", cli.ExitNotRun))
	got := strings.Join(lines, "\n") + "\n"
	file := filepath.Join("testdata", "freeze", "cli_tree.golden")
	if *update {
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("%v: run `just freeze`", err)
	}
	if string(want) != got {
		t.Fatalf("the command tree changed: review the diff of %s and accept a meant change with `just freeze`\n%s", file, firstDiff(string(want), got))
	}
}

func firstDiff(a, b string) string {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := 0; i < len(al) && i < len(bl); i++ {
		if al[i] != bl[i] {
			return fmt.Sprintf("line %d: golden %q, now %q", i+1, al[i], bl[i])
		}
	}
	return fmt.Sprintf("golden has %d lines, now %d", len(al), len(bl))
}

// Under -o json and -o yaml a failure is a structured error on stderr, and exec keeps its exit status for the command in
// the jail: a failure of JailOps itself is 125.
func TestMachineErrorAndNotRunStatus(t *testing.T) {
	h := newHarness(t)
	h.seed()
	_, errs, code := h.run("", "jail", "show", "nope", "-o", "json")
	if code != cli.ExitNotFound {
		t.Fatalf("exit %d: %s", code, errs)
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(errs), &body); err != nil || body.Error.Code != "not_found" || body.Error.Message == "" {
		t.Fatalf("not a structured error: %v: %q", err, errs)
	}
	_, errs, code = h.run("", "jail", "show", "-o", "json")
	if code != cli.ExitUsage || !strings.Contains(errs, `"code": "usage"`) {
		t.Fatalf("usage error: %d %q", code, errs)
	}
	_, _, code = h.run("", "exec", "nope", "--", "true")
	if code != cli.ExitNotRun {
		t.Fatalf("exec of a missing jail: exit %d, want %d", code, cli.ExitNotRun)
	}
}
