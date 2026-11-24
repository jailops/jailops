package service_test

import (
	"strings"
	"testing"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/internal/service"
	"jailops.net/jailops/sdk/api"
)

// A jail whose prompt record has no format number (an earlier release wrote the files) gets the prompt files again at its
// next start, once; a record with the current format leaves the files alone.
func TestOldPromptFormatIsRewrittenOnce(t *testing.T) {
	e, s := tickingEnv(t)
	alpha := as(globalAdmin, dtest.SeedProject)
	run := func(op api.Operation, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		if got := wait(t, s, op); got.Status != api.OpSuccess {
			t.Fatalf("%+v\n%s", got, e.Log())
		}
	}
	run(s.CreateJail(alpha, dtest.WebJail("web")))
	file := "/jailops/projects/alpha/jails/web/root" + api.PromptFile
	cycle := func() {
		run(s.JailAction(alpha, "web", api.JailAction{Action: "start"}))
		run(s.JailAction(alpha, "web", api.JailAction{Action: "stop"}))
	}
	cycle()
	if !strings.Contains(e.File(file), "PS1=") {
		t.Fatalf("no prompt file after the first start: %q", e.File(file))
	}
	// an unchanged mark: the files are left alone, even when somebody changed them
	e.Files.Put(file, []byte("edited\n"))
	cycle()
	if got := e.File(file); got != "edited\n" {
		t.Fatalf("a record of the current format rewrote the file: %q", got)
	}
	// a record without a format number: rewritten, then left alone again
	service.SetShellFormat(s, "alpha_web", 0)
	cycle()
	if got := e.File(file); !strings.Contains(got, "PS1=") {
		t.Fatalf("an old record did not rewrite the file: %q", got)
	}
	e.Files.Put(file, []byte("edited again\n"))
	cycle()
	if got := e.File(file); got != "edited again\n" {
		t.Fatalf("the file was rewritten twice: %q", got)
	}
}
