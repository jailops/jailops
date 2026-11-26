package server_test

import (
	"archive/tar"
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"jailops.net/jailops/internal/daemon"
	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

func jailFile(t *testing.T, e *dtest.Env, jail, rel, data string) {
	t.Helper()
	p := filepath.Join(e.JailRootDir("alpha", jail), rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func tarBytes(t *testing.T, name, body string, mode int64) io.Reader {
	t.Helper()
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	tw.Write([]byte(body))
	tw.Close()
	return &b
}

func readAll(t *testing.T, r io.Reader) []byte {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The files of a jail over HTTP: operators only (readonly is refused, like exec), the project rules of every jail
// route, a root that is not mounted is busy, and what is read and written stays below the jail root.
func TestJailFilesOverHTTP(t *testing.T) {
	e := seeded(t)
	c := ctxT(t)
	mustJail(t, e, "web")
	jailFile(t, e, "web", "etc/motd", "welcome\n")
	info, err := e.Admin.Info(c)
	if err != nil || !hasStr(info.APIExtensions, "jail_files") {
		t.Fatalf("extension: %v %v", err, info.APIExtensions)
	}
	ro := e.Client(api.PermReadonly)
	_, err = ro.Jails().FileStat(c, "web", "/etc/motd")
	wantCode(t, err, api.CodeForbidden)
	_, err = ro.Jails().FileGet(c, "web", "/etc/motd", sdk.FileGetOptions{})
	wantCode(t, err, api.CodeForbidden)
	_, err = ro.Jails().FilePut(c, "web", "/etc/x", strings.NewReader("x"), sdk.FilePutOptions{Length: 1})
	wantCode(t, err, api.CodeForbidden)
	op := e.Client(api.PermOperator)

	st, err := op.Jails().FileStat(c, "web", "/etc/motd")
	if err != nil || st.Type != api.FileTypeFile || st.Size != 8 || st.Mode != "0644" {
		t.Fatalf("%v %+v", err, st)
	}
	if st, err = op.Jails().FileStat(c, "web", "etc/nothing"); err != nil || st.Type != api.FileTypeNone {
		t.Fatalf("%v %+v", err, st)
	}
	if st, err = op.Jails().FileStat(c, "web", "/etc"); err != nil || st.Type != api.FileTypeDir {
		t.Fatalf("%v %+v", err, st)
	}

	// a plain download
	dl, err := op.Jails().FileGet(c, "web", "/etc/motd", sdk.FileGetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, dl); string(got) != "welcome\n" || dl.Size != 8 || dl.Mode != "0644" {
		t.Fatalf("%q size=%d mode=%s", got, dl.Size, dl.Mode)
	}
	dl.Close()
	// a directory is not a plain download
	_, err = op.Jails().FileGet(c, "web", "/etc", sdk.FileGetOptions{})
	wantCode(t, err, api.CodeInvalid)
	_, err = op.Jails().FileGet(c, "web", "/etc/missing", sdk.FileGetOptions{})
	wantCode(t, err, api.CodeNotFound)

	// a plain upload, with a mode
	res, err := op.Jails().FilePut(c, "web", "/etc/new.conf", strings.NewReader("k=v\n"), sdk.FilePutOptions{Length: 4, Mode: 0o600})
	if err != nil || res.Files != 1 || res.Bytes != 4 {
		t.Fatalf("%v %+v", err, res)
	}
	b, _ := os.ReadFile(filepath.Join(e.JailRootDir("alpha", "web"), "etc/new.conf"))
	if string(b) != "k=v\n" {
		t.Fatalf("%q", b)
	}
	if fi, _ := os.Stat(filepath.Join(e.JailRootDir("alpha", "web"), "etc/new.conf")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("%v", fi.Mode())
	}
	// a tar upload and a tar download
	res, err = op.Jails().FilePut(c, "web", "/etc", tarBytes(t, "from.tar", "tarred", 0o644), sdk.FilePutOptions{Archive: true})
	if err != nil || res.Files != 1 {
		t.Fatalf("%v %+v", err, res)
	}
	dl, err = op.Jails().FileGet(c, "web", "/etc/from.tar", sdk.FileGetOptions{Archive: true})
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(dl)
	h, err := tr.Next()
	if err != nil || h.Name != "from.tar" || string(readAll(t, tr)) != "tarred" {
		t.Fatalf("%+v %v", h, err)
	}
	if _, err := io.Copy(io.Discard, dl); err != nil {
		t.Fatalf("the end of a complete download: %v", err)
	}
	dl.Close()
	// a directory as a tar needs recursive
	_, err = op.Jails().FileGet(c, "web", "/etc", sdk.FileGetOptions{Archive: true})
	wantCode(t, err, api.CodeForbidden)
	dl, err = op.Jails().FileGet(c, "web", "/etc", sdk.FileGetOptions{Archive: true, Recursive: true, As: "etc-copy"})
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	tr = tar.NewReader(dl)
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		names[h.Name] = true
	}
	dl.Close()
	if !names["etc-copy/"] || !names["etc-copy/motd"] {
		t.Fatalf("%v", names)
	}
	// a missing directory is not_found, and made with mkdir
	_, err = op.Jails().FilePut(c, "web", "/opt/app", tarBytes(t, "f", "x", 0o644), sdk.FilePutOptions{Archive: true})
	wantCode(t, err, api.CodeNotFound)
	if _, err = op.Jails().FilePut(c, "web", "/opt/app", tarBytes(t, "f", "x", 0o644), sdk.FilePutOptions{Archive: true, Mkdir: true}); err != nil {
		t.Fatal(err)
	}
}

func TestJailFilesStayBelowTheRoot(t *testing.T) {
	e := seeded(t)
	c := ctxT(t)
	mustJail(t, e, "web")
	jailFile(t, e, "web", "etc/motd", "welcome\n")
	outside := filepath.Join(e.Dir, "host-secret")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := e.JailRootDir("alpha", "web")
	if err := os.Symlink(outside, filepath.Join(root, "etc", "leak")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(e.Dir, filepath.Join(root, "hostdir")); err != nil {
		t.Fatal(err)
	}
	op := e.Client(api.PermOperator)
	// a symlink is not followed to read, and a path through one is refused
	_, err := op.Jails().FileGet(c, "web", "/etc/leak", sdk.FileGetOptions{})
	wantCode(t, err, api.CodeInvalid)
	_, err = op.Jails().FileGet(c, "web", "/hostdir/host-secret", sdk.FileGetOptions{})
	if err == nil {
		t.Fatal("a symlink directory was followed")
	}
	// nor to write
	_, err = op.Jails().FilePut(c, "web", "/hostdir", tarBytes(t, "pwned", "x", 0o644), sdk.FilePutOptions{Archive: true})
	wantCode(t, err, api.CodeForbidden)
	if _, err := os.Stat(filepath.Join(e.Dir, "pwned")); err == nil {
		t.Fatal("written outside the jail")
	}
	// names in an archive
	for _, name := range []string{"../pwned", "/abs/pwned"} {
		_, err = op.Jails().FilePut(c, "web", "/etc", tarBytes(t, name, "x", 0o644), sdk.FilePutOptions{Archive: true})
		wantCode(t, err, api.CodeForbidden)
	}
	// the mux cleans . and .. before the handler: the path that answers is always one below the jail root
	tok := e.IdentityToken(api.PermOperator, true)
	for _, p := range []string{"/api/v1/jails/web/files/etc/../../x", "/api/v1/jails/web/files/etc//x", "/api/v1/jails/web/files/etc/%2e%2e/%2e%2e/x"} {
		code, body := e.Raw("GET", p+"?stat=true&project=alpha", tok, "")
		if code == 200 && strings.Contains(body, "..") {
			t.Errorf("%s: %d %s", p, code, body)
		}
	}
}

func TestJailFilesNeedAMountedRootAndTheProjectOfTheJail(t *testing.T) {
	e := seeded(t)
	c := ctxT(t)
	mustJail(t, e, "web") // its root holds nothing: not mounted
	op := e.Client(api.PermOperator)
	_, err := op.Jails().FileStat(c, "web", "/etc")
	wantCode(t, err, api.CodeBusy)
	_, err = op.Jails().FilePut(c, "web", "/etc", tarBytes(t, "f", "x", 0o644), sdk.FilePutOptions{Archive: true})
	wantCode(t, err, api.CodeBusy)
	jailFile(t, e, "web", "etc/motd", "x")
	net := e.MakeNetwork("beta")
	if _, err := e.Admin.Projects().Create(c, api.Project{Metadata: api.ObjectMeta{Name: "beta"}, Spec: api.ProjectSpec{
		Networks: []api.ProjectNetwork{{Network: net}}, Storage: []api.ProjectStorage{{Storage: "default"}}}}); err != nil {
		t.Fatal(err)
	}
	beta := e.Scoped(api.PermOperator, "beta")
	_, err = beta.Project("alpha").Jails().FileStat(c, "web", "/etc/motd")
	wantCode(t, err, api.CodeNotFound)
	_, err = beta.Project("beta").Jails().FileStat(c, "web", "/etc/motd")
	wantCode(t, err, api.CodeNotFound)
}

func TestJailFilesLimits(t *testing.T) {
	e := dtest.New(t, dtest.Options{Mutate: func(cfg *daemon.Config) { cfg.Limits.FileCopyBytes = 64 }})
	e.Seed()
	c := ctxT(t)
	mustJail(t, e, "web")
	jailFile(t, e, "web", "etc/motd", "x")
	op := e.Client(api.PermOperator)
	big := strings.Repeat("x", 200)
	_, err := op.Jails().FilePut(c, "web", "/etc/big", strings.NewReader(big), sdk.FilePutOptions{Length: int64(len(big))})
	wantCode(t, err, api.CodeInvalid)
	if !strings.Contains(err.Error(), "at most 64") {
		t.Fatalf("%v", err)
	}
	_, err = op.Jails().FilePut(c, "web", "/etc", tarBytes(t, "big", big, 0o644), sdk.FilePutOptions{Archive: true})
	wantCode(t, err, api.CodeInvalid)
	if _, serr := os.Stat(filepath.Join(e.JailRootDir("alpha", "web"), "etc/big")); serr == nil {
		t.Fatal("a file over the limit was written")
	}
	// a plain upload without a length
	tok := e.IdentityToken(api.PermOperator, true)
	req, _ := http.NewRequest("PUT", e.HTTP.URL+"/api/v1/jails/web/files/etc/x?project=alpha", io.NopCloser(strings.NewReader("x")))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.ContentLength = -1
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("without a length: %d", resp.StatusCode)
	}
}

func TestJailFilesAreAudited(t *testing.T) {
	e := seeded(t)
	c := ctxT(t)
	mustJail(t, e, "web")
	jailFile(t, e, "web", "etc/motd", "welcome\n")
	op := e.Client(api.PermOperator)
	if _, err := op.Jails().FilePut(c, "web", "/etc/a", strings.NewReader("a"), sdk.FilePutOptions{Length: 1}); err != nil {
		t.Fatal(err)
	}
	dl, err := op.Jails().FileGet(c, "web", "/etc/motd", sdk.FileGetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	readAll(t, dl)
	dl.Close()
	entries, err := e.Admin.Audit(c, 50)
	if err != nil {
		t.Fatal(err)
	}
	var put, get bool
	for _, a := range entries {
		put = put || strings.Contains(a.Detail, "file put /etc/a") && a.Project == "alpha"
		get = get || strings.Contains(a.Detail, "file get /etc/motd") && a.Project == "alpha"
	}
	if !put || !get {
		t.Fatalf("put=%v get=%v in %+v", put, get, entries)
	}
}

// The directory listing is the twin of ?stat=true: operators only, below the jail root, no symlink followed, capped,
// and it leaves no audit entry.
func TestJailFileListOverHTTP(t *testing.T) {
	e := seeded(t)
	c := ctxT(t)
	mustJail(t, e, "web")
	jailFile(t, e, "web", "etc/motd", "welcome\n")
	jailFile(t, e, "web", "etc/conf.d/x", "x")
	if err := os.Symlink("/", filepath.Join(e.JailRootDir("alpha", "web"), "etc", "up")); err != nil {
		t.Fatal(err)
	}
	info, err := e.Admin.Info(c)
	if err != nil || !hasStr(info.APIExtensions, "jail_files_list") {
		t.Fatalf("extension: %v %v", err, info.APIExtensions)
	}
	_, err = e.Client(api.PermReadonly).Jails().FileList(c, "web", "/etc")
	wantCode(t, err, api.CodeForbidden)
	op := e.Client(api.PermOperator)
	before, err := e.Admin.Audit(c, 100)
	if err != nil {
		t.Fatal(err)
	}
	l, err := op.Jails().FileList(c, "web", "/etc")
	if err != nil || l.Path != "/etc" || len(l.Entries) != 3 {
		t.Fatalf("%v %+v", err, l)
	}
	want := map[string]api.JailFileType{"conf.d": api.FileTypeDir, "motd": api.FileTypeFile, "up": api.FileTypeSymlink}
	for _, en := range l.Entries {
		if want[en.Name] != en.Type {
			t.Errorf("%+v", en)
		}
	}
	if l, err = op.Jails().FileList(c, "web", "/"); err != nil || len(l.Entries) == 0 {
		t.Fatalf("root: %v %+v", err, l)
	}
	_, err = op.Jails().FileList(c, "web", "/etc/motd")
	wantCode(t, err, api.CodeInvalid)
	_, err = op.Jails().FileList(c, "web", "/etc/missing")
	wantCode(t, err, api.CodeNotFound)
	_, err = op.Jails().FileList(c, "web", "/etc/up")
	wantCode(t, err, api.CodeForbidden)
	_, err = op.Jails().FileList(c, "web", "/etc/../x")
	wantCode(t, err, api.CodeInvalid)
	after, err := e.Admin.Audit(c, 100)
	if err != nil || len(after) != len(before) {
		t.Fatalf("a listing is audited: %d entries before, %d after (%v)", len(before), len(after), err)
	}
	// capped
	for i := 0; i < api.MaxFileListEntries+1; i++ {
		jailFile(t, e, "web", "many/f"+strconv.Itoa(i), "")
	}
	if l, err = op.Jails().FileList(c, "web", "/many"); err != nil || len(l.Entries) != api.MaxFileListEntries || !l.Truncated {
		t.Fatalf("%v %d %v", err, len(l.Entries), l.Truncated)
	}
}

func TestJailFileListNeedsAMountedRoot(t *testing.T) {
	e := seeded(t)
	c := ctxT(t)
	mustJail(t, e, "web") // its root holds nothing: not mounted
	_, err := e.Client(api.PermOperator).Jails().FileList(c, "web", "/etc")
	wantCode(t, err, api.CodeBusy)
}
