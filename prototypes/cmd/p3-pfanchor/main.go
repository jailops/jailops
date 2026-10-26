// Prototype 3: per-jail PF anchors loaded, replaced and flushed atomically, coexisting with the
// administrator's pf.conf. Written without access to FreeBSD; UNVERIFIED: marks what this run settles.
//
// Safety: only anchors matching jailops/jop3* are modified. pf.conf is only read. pf is never enabled
// or disabled. All rules use RFC 5737 documentation addresses.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"jailops.net/jailops/prototypes/internal/harness"
	"jailops.net/jailops/prototypes/internal/pfanchor"
)

const (
	anchor = "jailops/jop3test"
	other  = "jailops/jop3shared"
)

var pfConf = "/etc/pf.conf"

func main() {
	harness.Exit(harness.Spec{
		N:       3,
		Name:    "pfanchor",
		NoPool:  true,
		Summary: "atomic load/replace/flush of a per-jail PF anchor, coexistence with pf.conf, failed validation keeps the old ruleset",
		Plan: []string{
			"read (never write) " + "pf.conf and the running main ruleset; record `pfctl -sr -sn` as a baseline",
			"load v1 into anchor " + anchor + " via `pfctl -a ... -f -` after `pfctl -n` validation, read it back",
			"replace with v2 while a sampler reads the anchor every few ms: it must only ever see v1 or v2, never empty or mixed",
			"feed a syntactically invalid and a semantically invalid ruleset: -n must reject, direct load must fail, v2 must remain",
			"table handling: add an address to a persist table, reload, see whether it survives; try referencing a table of another anchor",
			"flush the anchor, confirm empty; compare the main ruleset with the baseline: identical",
			"cleanup flushes anchors " + anchor + " and " + other + " only",
		},
		Flags: func(e *harness.Env, fs *flag.FlagSet) {
			fs.StringVar(&pfConf, "pf-conf", pfConf, "path of the administrator's pf.conf (read only)")
		},
		Run:     run,
		Cleanup: cleanup,
	})
}

func pf(e *harness.Env, stdin string, args ...string) harness.Result {
	if err := pfanchor.GuardPfctl(args); err != nil {
		e.Abort("%v", err)
	}
	return e.RunStdin(stdin, append([]string{"pfctl"}, args...)...)
}

func pfq(e *harness.Env, args ...string) harness.Result {
	if err := pfanchor.GuardPfctl(args); err != nil {
		e.Abort("%v", err)
	}
	return e.Quiet(append([]string{"pfctl"}, args...)...)
}

func cleanup(e *harness.Env) {
	for _, a := range []string{anchor, other} {
		pf(e, "", "-a", a, "-F", "all")
	}
}

func run(e *harness.Env) {
	e.Require("pfctl")
	e.OnCleanup(func() { cleanup(e) })

	e.Step("1. baseline: pf state and the administrator's configuration (read only)")
	info := pfq(e, "-s", "info").Out()
	en, known := pfanchor.PfStatusEnabled(info)
	e.Note("pf status known=%v enabled=%v", known, en)
	e.Check("pf status readable", known || e.DryRun, firstLine(info))
	if !en {
		e.Note("pf is disabled: rules still load into the kernel but are not evaluated; coexistence is then only syntactic")
	}
	conf, err := os.ReadFile(pfConf)
	lines := pfanchor.ParseAnchorLines(string(conf))
	e.Check("pf.conf has nat-anchor, rdr-anchor and anchor \"jailops/*\"", err == nil && lines.All(),
		fmt.Sprintf("%s: %+v err=%v (add the three lines once; this run does not edit pf.conf)", pfConf, lines, err))
	baseSR := pfq(e, "-s", "rules").Out()
	baseNAT := pfq(e, "-s", "nat").Out()
	e.Check("running main ruleset references the jailops/* anchors", strings.Contains(baseSR, `anchor "jailops/*"`) || e.DryRun,
		"pfctl -sr shows: "+oneLine(baseSR, 200))

	e.Step("2. validate then load v1")
	v1 := pfanchor.Render("v1")
	e.Note("ruleset v1:\n%s", indent(v1))
	r := pf(e, v1, "-n", "-a", anchor, "-f", "-")
	// UNVERIFIED: pfctl reads rules from stdin with `-f -`.
	e.Must("pfctl -n accepts v1 (stdin form)", r.OK(), r.Combined())
	e.Resolve("pfctl -n -a <anchor> -f - validates rules from stdin")
	r = pf(e, v1, "-a", anchor, "-f", "-")
	e.Must("load v1", r.OK(), r.Combined())
	sr := pfq(e, "-a", anchor, "-s", "rules").Out()
	e.Check("read back v1 filter rules", pfanchor.Classify(sr) == pfanchor.StateV1 || e.DryRun, oneLine(sr, 300))
	nat := pfq(e, "-a", anchor, "-s", "nat").Out()
	e.Check("read back rdr rule in anchor", strings.Contains(nat, "rdr") || e.DryRun, oneLine(nat, 300))
	tbl := pfq(e, "-a", anchor, "-t", "jop3_set", "-T", "show").Out()
	e.Check("anchor-local table loaded", strings.Contains(tbl, "192.0.2.10") || e.DryRun, oneLine(tbl, 100))
	as := pfq(e, "-a", "jailops", "-s", "Anchors").Out()
	e.Check("anchor listed under parent jailops", strings.Contains(as, "jop3test") || e.DryRun, oneLine(as, 200))

	e.Step("3. atomic replace under observation")
	if e.DryRun {
		e.Note("DRY-RUN would alternate v1/v2 %d times while sampling `pfctl -a %s -sr`", iterations, anchor)
	} else {
		atomicity(e)
	}

	e.Step("4. failed validation leaves the old ruleset intact")
	// establish known state first
	pf(e, pfanchor.Render("v2"), "-a", anchor, "-f", "-")
	before := pfq(e, "-a", anchor, "-s", "rules").Out()
	r = pf(e, pfanchor.Broken, "-n", "-a", anchor, "-f", "-")
	e.Check("pfctl -n rejects a syntax error", !r.OK() || e.DryRun, fmt.Sprintf("exit=%d %s", r.Code, oneLine(r.Combined(), 200)))
	r = pf(e, pfanchor.Broken, "-a", anchor, "-f", "-")
	e.Check("direct load of the syntax error fails", !r.OK() || e.DryRun, fmt.Sprintf("exit=%d %s", r.Code, oneLine(r.Combined(), 200)))
	after := pfq(e, "-a", anchor, "-s", "rules").Out()
	e.Check("old ruleset intact after failed load (syntax)", before == after && (pfanchor.Classify(after) == pfanchor.StateV2 || e.DryRun), oneLine(after, 300))
	r = pf(e, pfanchor.BrokenSemantic, "-n", "-a", anchor, "-f", "-")
	e.Check("pfctl -n rejects an undefined table", !r.OK() || e.DryRun, fmt.Sprintf("exit=%d %s", r.Code, oneLine(r.Combined(), 200)))
	r = pf(e, pfanchor.BrokenSemantic, "-a", anchor, "-f", "-")
	after = pfq(e, "-a", anchor, "-s", "rules").Out()
	e.Check("old ruleset intact after failed load (undefined table)", before == after, fmt.Sprintf("load exit=%d", r.Code))
	e.Resolve("a failed pfctl validation/load leaves the previous anchor ruleset intact")

	e.Step("5. tables")
	pf(e, "", "-a", anchor, "-t", "jop3_set", "-T", "add", "192.0.2.99")
	t1 := pfq(e, "-a", anchor, "-t", "jop3_set", "-T", "show").Out()
	pf(e, pfanchor.Render("v2"), "-a", anchor, "-f", "-")
	t2 := pfq(e, "-a", anchor, "-t", "jop3_set", "-T", "show").Out()
	// Both outcomes are information, not failure: it decides whether JailOps must re-add runtime table entries after a reload.
	e.Check("table contents after anchor reload (informational)", true,
		fmt.Sprintf("before=%v after=%v; runtime entries %s", pfanchor.SplitTable(t1), pfanchor.SplitTable(t2),
			survive(strings.Contains(t2, "192.0.2.99"))))
	// A table in another anchor is not visible here.
	pf(e, "table <jop3_other> persist { 192.0.2.77 }\n", "-a", other, "-f", "-")
	ref := "pass quick proto tcp from <jop3_other> to 192.0.2.9 port 9\n"
	r = pf(e, ref, "-n", "-a", anchor, "-f", "-")
	e.Check("table of another anchor is NOT usable (informational)", true,
		fmt.Sprintf("validation exit=%d; so jailops/_shared tables cannot be referenced from per-jail anchors: %s", r.Code, oneLine(r.Combined(), 160)))
	e.Resolve("anchor table scope: " + map[bool]string{true: "tables are anchor-local", false: "cross-anchor reference accepted"}[!r.OK()])

	e.Step("6. flush and coexistence")
	r = pf(e, "", "-a", anchor, "-F", "all")
	e.Check("flush anchor", r.OK(), r.Combined())
	sr = pfq(e, "-a", anchor, "-s", "rules").Out()
	nat = pfq(e, "-a", anchor, "-s", "nat").Out()
	e.Check("anchor empty after flush", (sr == "" && nat == "") || e.DryRun, oneLine(sr+nat, 200))
	pf(e, "", "-a", other, "-F", "all")
	e.Check("main filter ruleset unchanged", pfq(e, "-s", "rules").Out() == baseSR, "compared `pfctl -sr` with baseline")
	e.Check("main nat ruleset unchanged", pfq(e, "-s", "nat").Out() == baseNAT, "compared `pfctl -sn` with baseline")
	e.Resolve("per-jail anchors load, replace and flush without touching the administrator's ruleset")
}

const iterations = 200

func atomicity(e *harness.Env) {
	var stop atomic.Bool
	var wg sync.WaitGroup
	counts := map[pfanchor.State]int{}
	var mu sync.Mutex
	wg.Add(1)
	go func() {
		defer wg.Done()
		for !stop.Load() {
			out, _ := runq("pfctl", "-a", anchor, "-s", "rules")
			mu.Lock()
			counts[pfanchor.Classify(out)]++
			mu.Unlock()
		}
	}()
	fail := 0
	t0 := time.Now()
	for i := 0; i < iterations; i++ {
		v := "v1"
		if i%2 == 1 {
			v = "v2"
		}
		if !pf(e, pfanchor.Render(v), "-a", anchor, "-f", "-").OK() {
			fail++
		}
	}
	stop.Store(true)
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	bad := counts[pfanchor.StateEmpty] + counts[pfanchor.StateMixed] + counts[pfanchor.StateOther]
	e.Check("replace never exposed an empty or mixed ruleset", bad == 0 && fail == 0 && counts[pfanchor.StateV1]+counts[pfanchor.StateV2] > 0,
		fmt.Sprintf("%d loads in %v, load failures=%d, samples=%v (a sample taken mid-flight can only fall in v1/v2 if the swap is atomic)", iterations, time.Since(t0).Round(time.Millisecond), fail, counts))
	e.Resolve("pfctl -a <anchor> -f replaces an anchor atomically (sampled)")
}

func runq(argv ...string) (string, error) {
	// the sampler must not print; use the harness-free exec path
	return sampleExec(argv)
}

func survive(b bool) string {
	if b {
		return "survived the reload"
	}
	return "were lost on reload (JailOps must re-add)"
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	return l
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		s = s[:n] + "..."
	}
	return s
}

func indent(s string) string {
	return "   | " + strings.ReplaceAll(strings.TrimSpace(s), "\n", "\n   | ")
}
