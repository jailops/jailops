package server_test

import (
	"strings"
	"testing"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/sdk/api"
)

// Jail logs over HTTP: readonly is forbidden (logs hold secrets), operator
// reads them in its project only, and the file name and query are checked
// before anything is read.
func TestJailLogsOverHTTP(t *testing.T) {
	e := seeded(t)
	c := ctxT(t)
	mustJail(t, e, "web")
	const root = "/jailops/projects/alpha/jails/web/root"
	e.Files.Put(root+"/var/log/messages", []byte("Oct 29 11:00:00 web one\nOct 29 11:30:00 web two\n"))
	e.Files.Put(root+"/var/log/nginx/error.log", []byte("err\n"))
	info, err := e.Admin.Info(c)
	if err != nil || !hasStr(info.APIExtensions, "jail_logs") {
		t.Fatalf("extension: %v %v", err, info.APIExtensions)
	}
	ro := e.Client(api.PermReadonly)
	_, err = ro.Jails().Logs(c, "web")
	wantCode(t, err, api.CodeForbidden)
	_, err = ro.Jails().Log(c, "web", "messages", api.JailLogQuery{})
	wantCode(t, err, api.CodeForbidden)
	op := e.Client(api.PermOperator)
	files, err := op.Jails().Logs(c, "web")
	if err != nil || len(files) != 2 || files[0].Name != "messages" || files[0].Size != 48 || files[0].Kind != api.JailLogActive || files[1].Name != "nginx/error.log" {
		t.Fatalf("%v %+v", err, files)
	}
	r, err := op.Jails().Log(c, "web", "messages", api.JailLogQuery{Tail: 1})
	if err != nil || len(r.Lines) != 1 || r.Lines[0].Text != "Oct 29 11:30:00 web two" || r.Lines[0].N != 2 || r.NextOffset != 48 || !r.Truncated {
		t.Fatalf("%v %+v", err, r)
	}
	r, err = op.Jails().Log(c, "web", "nginx/error.log", api.JailLogQuery{Since: "1d", Grep: "ERR"})
	if err != nil || len(r.Lines) != 1 {
		t.Fatalf("%v %+v", err, r)
	}
	// another project's operator: the jail does not exist for it
	net := e.MakeNetwork("beta")
	if _, err := e.Admin.Projects().Create(c, api.Project{Metadata: api.ObjectMeta{Name: "beta"}, Spec: api.ProjectSpec{
		Networks: []api.ProjectNetwork{{Network: net}}, Storage: []api.ProjectStorage{{Storage: "default"}}}}); err != nil {
		t.Fatal(err)
	}
	beta := e.Scoped(api.PermOperator, "beta")
	_, err = beta.Project("alpha").Jails().Logs(c, "web")
	wantCode(t, err, api.CodeNotFound)
	_, err = beta.Project("alpha").Jails().Log(c, "web", "messages", api.JailLogQuery{})
	wantCode(t, err, api.CodeNotFound)
	_, err = beta.Project("beta").Jails().Logs(c, "web")
	wantCode(t, err, api.CodeNotFound)
	// bad names and queries are refused at the door
	tok := e.IdentityToken(api.PermOperator, true)
	for _, path := range []string{
		"/api/v1/jails/web/logs/.hidden", "/api/v1/jails/web/logs/a/b/c", "/api/v1/jails/web/logs/messages?tail=0",
		"/api/v1/jails/web/logs/messages?tail=5001", "/api/v1/jails/web/logs/messages?since=later", "/api/v1/jails/web/logs/messages?offset=-1",
		"/api/v1/jails/web/logs/messages?grep=" + strings.Repeat("a", 129),
	} {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		if code, body := e.Raw("GET", path+sep+"project=alpha", tok, ""); code != 400 || !strings.Contains(body, "invalid") {
			t.Errorf("%s: %d %s", path, code, body)
		}
	}
	if code, body := e.Raw("GET", "/api/v1/jails/web/logs/nope?project=alpha", tok, ""); code != 404 || !strings.Contains(body, "no log file") {
		t.Errorf("missing file: %d %s", code, body)
	}
	// the audit log has the reads, under the operator's identity
	entries, err := e.Admin.Audit(c, 50)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, a := range entries {
		if strings.HasPrefix(a.Action, "LOGS ") && a.Project == "alpha" && a.Resource == "/api/v1/jails/web" {
			n++
		}
	}
	if n < 3 {
		t.Fatalf("expected the list and both reads in the audit log, got %d entries", n)
	}
}

// A stopped jail whose root is not mounted answers busy with the dataset to mount.
func TestJailLogsOfAnUnmountedJail(t *testing.T) {
	e := seeded(t)
	c := ctxT(t)
	mustJail(t, e, "web")
	if err := e.Files.RemoveAll(c, "/jailops/projects/alpha/jails/web/root"); err != nil {
		t.Fatal(err)
	}
	op := e.Client(api.PermOperator)
	_, err := op.Jails().Logs(c, "web")
	wantCode(t, err, api.CodeBusy)
	if ae := err.(*api.Error); !strings.Contains(ae.Message, "not mounted") || !strings.Contains(ae.Hint, "zfs mount") {
		t.Fatalf("%+v", ae)
	}
	_, err = op.Jails().Log(c, "web", "messages", api.JailLogQuery{})
	wantCode(t, err, api.CodeBusy)
	_ = dtest.LogModTime
}
