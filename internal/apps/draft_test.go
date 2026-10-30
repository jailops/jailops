package apps

import (
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// draftFacts is what a jail that runs memcached and a small web site
// answers, with the noise a real jail has: base services, base logs,
// dependencies, a secret knob, rotated logs, sample files.
func draftFacts() DraftFacts {
	return DraftFacts{
		Jail: "cache",
		Packages: []byte("memcached 1.6.39 FreeBSD https://memcached.org/\n" +
			"FreeBSD-runtime 15.1 FreeBSD-base \n" +
			"pkg 2.4.2 FreeBSD https://github.com/freebsd/pkg\n" +
			"jailops-tool 0.2.0 jailops https://example.com/tool\n" +
			"bad;name 1 FreeBSD\n"),
		RcConf: []byte("hostname=\"cache\"\nsshd_enable=\"YES\"\nsendmail_enable=\"NONE\"\n" +
			"memcached_enable=\"YES\"\nmemcached_flags=\"-l 0.0.0.0 -m 64\"\nmemcached_password='hunter2'\n" +
			"# a comment\nnginx_enable=YES\nnginx_flags=\"$(id)\" # shell\nfoo_enable=\"NO\"\n" +
			"nginx_profiles=\"a b\"\n"),
		Sockets: []byte("root     sshd       779   3  tcp6   *:22                  *:*\n" +
			"root     sshd       779   4  tcp4   *:22                  *:*\n" +
			"nobody   memcached  801   26 tcp4   *:11211               *:*\n" +
			"nobody   memcached  801   27 tcp6   *:11211               *:*\n" +
			"www      nginx      900   6  tcp4   127.0.0.1:8080        *:*\n" +
			"www      nginx      900   7  tcp4   10.0.0.5:80           *:*\n" +
			"www      nginx      900   8  udp4   *:80                  *:*\n" +
			"garbage line\n"),
		Volumes: []string{"/var/db/memcached", "/srv/www", "/var/db"},
		Configs: []byte("/usr/local/etc/nginx/nginx.conf\n/usr/local/etc/nginx/nginx.conf.sample\n/usr/local/etc/rc.d/nginx\n" +
			"/usr/local/etc/memcached.conf\nrelative\n/usr/local/etc/../etc/passwd\n"),
		Logs:      []byte("/var/log/messages\n/var/log/nginx/access.log\n/var/log/nginx/error.log\n/var/log/nginx/access.log.0.bz2\n/var/log/cron\n/var/log/nginx/error.log.1\n"),
		Owners:    []byte("nobody:nobody /var/db/memcached\nwww:www /var/log/nginx\nroot:wheel /var/run/memcached\n1001:1001 /var/run/nginx\nwww:www /etc/passwd\n"),
		Resources: api.Resources{Memory: "512M", CPU: 2},
		Errs:      map[string]string{"owners": "stat: /var/run/memcached: No such file\nor directory"},
	}
}

// A draft is built from what the jail answered: the app's packages,
// services, knobs, ports, volumes, files and owners, and none of the base
// system's; it validates, and secrets stay out.
func TestBuildDraft(t *testing.T) {
	d := BuildDraft(draftFacts())
	a := d.Definition
	if err := Validate(&a); err != nil {
		t.Fatal(err)
	}
	if a.Name != "cache" || a.Homepage != "https://memcached.org/" {
		t.Errorf("%+v", a)
	}
	if !reflect.DeepEqual(a.Packages, []api.AppPackage{{Name: "memcached", Repo: "ports", Version: "1.6.39"}, {Name: "jailops-tool", Repo: "jailops", Version: "0.2.0"}}) {
		t.Errorf("packages %+v", a.Packages)
	}
	// nginx_flags holds an expansion: left out
	if len(a.Services) != 2 || a.Services[0].Name != "memcached" || a.Services[1].Name != "nginx" ||
		!reflect.DeepEqual(a.Services[0].RC, map[string]string{"memcached_flags": "-l 0.0.0.0 -m 64", "memcached_password": "TODO"}) ||
		!reflect.DeepEqual(a.Services[1].RC, map[string]string{"nginx_profiles": "a b"}) {
		t.Errorf("services %+v", a.Services)
	}
	want := []api.AppPort{
		{Name: "nginx", Port: 80, Proto: "tcp", Description: "Listened on by nginx."},
		{Name: "nginx-80", Port: 80, Proto: "udp", Description: "Listened on by nginx."},
		{Name: "nginx-8080", Port: 8080, Proto: "tcp", Description: "Listened on by nginx on localhost only."},
		{Name: "memcached", Port: 11211, Proto: "tcp", Description: "Listened on by memcached."},
	}
	if !reflect.DeepEqual(a.Ports, want) {
		t.Errorf("ports %+v", a.Ports)
	}
	if a.Health == nil || a.Health.TCP != 80 {
		t.Errorf("health %+v", a.Health)
	}
	if !reflect.DeepEqual(a.Data, []api.AppPath{{Path: "/var/db/memcached"}}) {
		t.Errorf("data %+v", a.Data)
	}
	if !reflect.DeepEqual(a.Config, []api.AppConfig{{Path: "/usr/local/etc/memcached.conf", Restart: "memcached"}, {Path: "/usr/local/etc/nginx/nginx.conf", Restart: "nginx"}}) {
		t.Errorf("config %+v", a.Config)
	}
	if !reflect.DeepEqual(a.Logs, []api.AppLog{{Name: "access", Path: "/var/log/nginx/access.log"}, {Name: "error", Path: "/var/log/nginx/error.log"}}) {
		t.Errorf("logs %+v", a.Logs)
	}
	if !reflect.DeepEqual(a.Setup, [][]string{{"/bin/mkdir", "-p", "/var/log/nginx"}, {"/usr/sbin/chown", "nobody:nobody", "/var/db/memcached"}, {"/usr/sbin/chown", "www:www", "/var/log/nginx"}}) {
		t.Errorf("setup %+v", a.Setup)
	}
	if a.Resources != (api.AppResources{Memory: "512M", Disk: "2G", CPU: "2"}) {
		t.Errorf("%+v", a.Resources)
	}
	for _, k := range []string{"category", "health", "data", "config", "setup", "services"} {
		if d.Todo[k] == "" {
			t.Errorf("no TODO for %s", k)
		}
	}
	text := strings.Join(d.Findings, "\n") + "\n"
	for _, w := range []string{"hunter2", "$(id)"} {
		if strings.Contains(text, w) || strings.Contains(string(Draft{App: a, Todo: d.Todo, Header: d.Findings}.YAML()), w) {
			t.Errorf("%q leaked", w)
		}
	}
	for _, w := range []string{"memcached_password look like secrets", "22/tcp of sshd", "volume /srv/www left out", "volume /var/db left out",
		"not knobs of an enabled service, left out: hostname, sendmail_enable\n", "owners: could not be read: stat: /var/run/memcached: No such file or directory",
		"/var/run/nginx is owned by 1001:1001"} {
		if !strings.Contains(text, w) {
			t.Errorf("findings lack %q:\n%s", w, text)
		}
	}

	out := Draft{App: a, Todo: d.Todo, Header: ExportHeader(d)}.YAML()
	if _, err := Parse("cache", out); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	path := filepath.Join("testdata", "draft.golden")
	if *update {
		os.WriteFile(path, out, 0o644)
	}
	if g, err := os.ReadFile(path); err != nil || string(g) != string(out) {
		t.Errorf("differs from %s (go test -update):\n%s", path, out)
	}
}

// A jail with nothing to show still drafts a valid definition, with TODO
// markers where a person must fill in.
func TestBuildDraftEmpty(t *testing.T) {
	d := BuildDraft(DraftFacts{Jail: "Bad_Name"})
	a := d.Definition
	if err := Validate(&a); err != nil {
		t.Fatal(err)
	}
	if a.Name != "app" || a.Packages[0].Name != TodoMarker || a.Health != nil || len(a.Services) != 0 {
		t.Errorf("%+v", a)
	}
	for _, k := range []string{"name", "packages", "services", "ports", "logs", "health"} {
		if d.Todo[k] == "" {
			t.Errorf("no TODO for %s", k)
		}
	}
	if d.Findings == nil {
		t.Error("findings is nil")
	}
}

func TestParseRcConf(t *testing.T) {
	got := ParseRcConf([]byte("a=\"1\"\n  b='two words'\nc=3 # comment\nd=\"x\\\"y\"\nbad name=1\ne=\"open\nf=$(id)\na=\"again\"\nUPPER=1\ng=\"${x}\"\nh=\"`id`\"\ni='$x'\n"))
	want := []RcSetting{{"a", "again"}, {"b", "two words"}, {"c", "3"}, {"d", `x"y`}, {"i", "$x"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v", got)
	}
}

func TestParseSockstat(t *testing.T) {
	got := ParseSockstat([]byte("root sshd 1 3 tcp46 *:22 *:*\nu p 1 3 udp6 ::1:53 *:*\nu q 1 3 tcp6 [fe80::1%lo0]:99 *:*\nu x 1 3 tcp4 127.0.0.1:0 *:*\nu y 1 3 stream /tmp/s\n"))
	want := []Listener{{"sshd", "tcp", 22, false}, {"p", "udp", 53, true}, {"q", "tcp", 99, false}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v", got)
	}
}
