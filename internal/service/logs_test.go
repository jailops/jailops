package service_test

import (
	"context"
	"strings"
	"testing"

	"jailops.net/jailops/internal/daemon"
	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/internal/service"
	"jailops.net/jailops/sdk/api"
)

const (
	webRoot     = "/jailops/projects/alpha/jails/web/root"
	webMessages = "Oct 29 11:00:00 web sshd[1]: Accepted\nOct 29 11:50:00 web nginx[3]: Error: upstream\nOct 29 11:55:00 web sshd[1]: token=abc\n"
)

func seedLogs(e *dtest.Env) {
	e.Files.Put(webRoot+"/var/log/messages", []byte(webMessages))
	e.Files.Put(webRoot+"/var/log/messages.0", []byte("old\n"))
	e.Files.Put(webRoot+"/var/log/messages.1.gz", []byte("zz"))
	e.Files.Put(webRoot+"/var/log/nginx/access.log", []byte("GET /\n"))
	e.Files.Put(webRoot+"/var/log/deep/er/x.log", []byte("no\n"))
	e.Files.Put(webRoot+"/etc/master.passwd", []byte("root:secret\n"))
}

// makeProject creates a second project with a network of its own.
func makeProject(t *testing.T, e *dtest.Env, name string) {
	t.Helper()
	net := e.MakeNetwork(name)
	if _, err := e.Admin.Projects().Create(context.Background(), api.Project{Metadata: api.ObjectMeta{Name: name}, Spec: api.ProjectSpec{
		Networks: []api.ProjectNetwork{{Network: net}}, Storage: []api.ProjectStorage{{Storage: "default"}}}}); err != nil {
		t.Fatal(err)
	}
}

func makeJail(t *testing.T, e *dtest.Env, s *service.Service, project, name string) {
	t.Helper()
	ctx := as(globalAdmin, project)
	op, err := s.CreateJail(ctx, dtest.WebJail(name))
	if err != nil {
		t.Fatal(err)
	}
	if got := wait(t, s, op); got.Status != api.OpSuccess {
		t.Fatalf("create: %+v\n%s", got.Error, e.Log())
	}
}

// Logs are read in the project of the request: another project's identity
// sees not_found, an identity without scope nothing, and the reply names
// only regular files one directory deep.
func TestJailLogsScope(t *testing.T) {
	e, s := newEnv(t)
	makeJail(t, e, s, dtest.SeedProject, "web")
	seedLogs(e)
	files, _, err := s.JailLogs(as(alphaOnly, dtest.SeedProject), "web")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range files {
		names = append(names, f.Name)
	}
	if got := strings.Join(names, " "); got != "messages messages.0 messages.1.gz nginx/access.log" {
		t.Fatalf("listed %q", got)
	}
	beta := service.Actor{Name: "bob", Kind: "user", Perm: api.PermOperator, Projects: []string{"beta"}}
	makeProject(t, e, "beta")
	_, _, err = s.JailLogs(as(beta, dtest.SeedProject), "web")
	wantCode(t, err, api.CodeNotFound)
	_, err = s.JailLog(as(beta, dtest.SeedProject), "web", "messages", api.JailLogQuery{})
	wantCode(t, err, api.CodeNotFound)
	_, _, err = s.JailLogs(as(beta, "beta"), "web")
	wantCode(t, err, api.CodeNotFound)
	if _, _, err := s.JailLogs(as(nobody, dtest.SeedProject), "web"); err == nil {
		t.Fatal("an identity without scope read logs")
	}
	_, _, err = s.JailLogs(as(alphaOnly, dtest.SeedProject), "ghost")
	wantCode(t, err, api.CodeNotFound)
}

func TestJailLogRead(t *testing.T) {
	e, s := newEnv(t)
	makeJail(t, e, s, dtest.SeedProject, "web")
	seedLogs(e)
	ctx := as(alphaOnly, dtest.SeedProject)
	size := int64(len(webMessages))
	r, err := s.JailLog(ctx, "web", "messages", api.JailLogQuery{})
	if err != nil || len(r.Lines) != 3 || r.Lines[0].N != 1 || r.NextOffset != size || r.Truncated || r.Rotated || r.Jail != "web" || r.File != "messages" {
		t.Fatalf("%v %+v", err, r)
	}
	r, err = s.JailLog(ctx, "web", "messages", api.JailLogQuery{Tail: 1, Grep: "ERROR"})
	if err != nil || len(r.Lines) != 1 || !strings.Contains(r.Lines[0].Text, "upstream") {
		t.Fatalf("grep: %v %+v", err, r)
	}
	r, err = s.JailLog(ctx, "web", "nginx/access.log", api.JailLogQuery{})
	if err != nil || len(r.Lines) != 1 || r.Lines[0].Text != "GET /" {
		t.Fatalf("subdirectory: %v %+v", err, r)
	}
	r, err = s.JailLog(ctx, "web", "messages", api.JailLogQuery{Offset: size})
	if err != nil || len(r.Lines) != 0 || r.NextOffset != size {
		t.Fatalf("follow: %v %+v", err, r)
	}
	e.Files.Put(webRoot+"/var/log/messages", []byte("new\n"))
	r, err = s.JailLog(ctx, "web", "messages", api.JailLogQuery{Offset: size})
	if err != nil || !r.Rotated || len(r.Lines) != 1 || r.Lines[0].Text != "new" || r.NextOffset != 4 {
		t.Fatalf("rotated: %v %+v", err, r)
	}
	for file, code := range map[string]api.Code{
		"messages.1.gz": api.CodeInvalid, "nope": api.CodeNotFound, "deep/er/x.log": api.CodeInvalid,
		"../etc/master.passwd": api.CodeInvalid, "/etc/passwd": api.CodeInvalid, ".hidden": api.CodeInvalid, "": api.CodeInvalid,
	} {
		_, err := s.JailLog(ctx, "web", file, api.JailLogQuery{})
		if !api.IsCode(err, code) {
			t.Errorf("%q: want %s, got %v", file, code, err)
		}
	}
	if _, err := s.JailLog(ctx, "web", "messages", api.JailLogQuery{Tail: api.MaxLogTail + 1}); !api.IsCode(err, api.CodeInvalid) {
		t.Errorf("tail limit: %v", err)
	}
	// a stopped jail whose root is not mounted
	if err := e.Files.RemoveAll(context.Background(), webRoot); err != nil {
		t.Fatal(err)
	}
	_, _, err = s.JailLogs(ctx, "web")
	wantCode(t, err, api.CodeBusy)
	if ae := err.(*api.Error); !strings.Contains(ae.Message, "not mounted") || !strings.Contains(ae.Hint, "zfs mount tank/jailops/projects/alpha/jails/web/root") {
		t.Fatalf("%+v", ae)
	}
	_, err = s.JailLog(ctx, "web", "messages", api.JailLogQuery{})
	wantCode(t, err, api.CodeBusy)
}

// Every read is audited, but one identity following one file is one entry per window, not one per poll.
func TestJailLogReadsAreAudited(t *testing.T) {
	e, s := newEnv(t)
	makeJail(t, e, s, dtest.SeedProject, "web")
	seedLogs(e)
	ann := as(alphaOnly, dtest.SeedProject)
	bob := as(service.Actor{Name: "bob", Kind: "token", Perm: api.PermOperator, Projects: []string{dtest.SeedProject}}, dtest.SeedProject)
	for i := 0; i < 3; i++ {
		if _, err := s.JailLog(ann, "web", "messages", api.JailLogQuery{Offset: int64(i)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.JailLog(ann, "web", "messages.0", api.JailLogQuery{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.JailLog(bob, "web", "messages", api.JailLogQuery{Grep: "token"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.JailLogs(ann, "web"); err != nil {
		t.Fatal(err)
	}
	entries, err := s.ListAudit(as(globalAdmin, ""), service.AuditQuery{})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, a := range entries {
		if strings.HasPrefix(a.Action, "LOGS ") {
			got = append(got, a.Requestor+" "+a.Action+" "+a.Resource+" "+a.Project+" "+a.Detail)
		}
	}
	want := []string{
		"ann LOGS /api/v1/jails/{name}/logs /api/v1/jails/web alpha list",
		"bob LOGS /api/v1/jails/{name}/logs/{file...} /api/v1/jails/web alpha read messages tail=200 since=\"\" grep=\"token\" offset=0",
		"ann LOGS /api/v1/jails/{name}/logs/{file...} /api/v1/jails/web alpha read messages.0 tail=200 since=\"\" grep=\"\" offset=0",
		"ann LOGS /api/v1/jails/{name}/logs/{file...} /api/v1/jails/web alpha read messages tail=200 since=\"\" grep=\"\" offset=0",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("audit:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestJailLogsWithoutAReader(t *testing.T) {
	e := dtest.New(t, dtest.Options{Clients: func(c *daemon.Clients) { c.Logs = nil }})
	e.Seed()
	s := e.D.Service()
	makeJail(t, e, s, dtest.SeedProject, "web")
	_, _, err := s.JailLogs(as(alphaOnly, dtest.SeedProject), "web")
	wantCode(t, err, api.CodeUnsupported)
}
