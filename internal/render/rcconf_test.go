package render

import (
	"strings"
	"testing"
)

func TestRcConfSet(t *testing.T) {
	existing := "# the jail's rc.conf\nhostname=\"db\"\n  postgresql_enable=\"NO\"\nsshd_enable=\"YES\"\npostgresql_enable=\"NO\" # a duplicate\n"
	got, err := RcConfSet(existing, []RcVar{
		{"postgresql_enable", "YES"},
		{"postgresql_initdb_flags", `--encoding=utf-8 --data-checksums "quoted" $HOME ` + "`id`" + ` back\slash`},
		{"nginx_enable", "YES"},
	})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "rcconf_set", got)
	// An empty file gets the assignments only; the result always ends with a newline.
	got, err = RcConfSet("", []RcVar{{"nginx_enable", "YES"}})
	if err != nil || got != "nginx_enable=\"YES\"\n" {
		t.Fatalf("%q %v", got, err)
	}
	if got, _ = RcConfSet("\n\n", []RcVar{{"nginx_enable", "YES"}}); got != "nginx_enable=\"YES\"\n" {
		t.Fatalf("%q", got)
	}
	// Setting the same value twice is idempotent.
	again, err := RcConfSet(got, []RcVar{{"nginx_enable", "YES"}})
	if err != nil || again != got {
		t.Fatalf("%q %v", again, err)
	}
	for _, bad := range [][]RcVar{
		{{"Nginx_enable", "YES"}}, {{"nginx-enable", "YES"}}, {{"", "YES"}}, {{"nginx_enable", "YES"}, {"nginx_enable", "NO"}},
		{{"nginx_enable", "YES\nsshd_enable=YES"}}, {{"nginx_enable", "a\x00b"}}, {{"x=y", "1"}}, {{strings.Repeat("a", 65), "1"}},
	} {
		if _, err := RcConfSet("", bad); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

// RcConfGet reads what RcConfSet writes, and the plain forms an administrator or sysrc(8) writes.
func TestRcConfGet(t *testing.T) {
	file := "# comment\nnginx_enable=\"YES\"\nsshd_enable=NO # off\n  cron_flags='-J 60'\nesc=\"a\\\"b\\$c\"\nnginx_enable=\"NO\"\nempty=\"\"\n"
	for name, want := range map[string]struct {
		v  string
		ok bool
	}{
		"nginx_enable": {"NO", true}, // the last assignment wins, as in sh
		"sshd_enable":  {"NO", true},
		"cron_flags":   {"-J 60", true},
		"esc":          {`a"b$c`, true},
		"empty":        {"", true},
		"missing":      {"", false},
	} {
		if v, ok := RcConfGet(file, name); v != want.v || ok != want.ok {
			t.Errorf("%s: %q %v, want %q %v", name, v, ok, want.v, want.ok)
		}
	}
	out, err := RcConfSet("", []RcVar{{Name: "a_flags", Value: `x "y" $z`}})
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := RcConfGet(out, "a_flags"); !ok || v != `x "y" $z` {
		t.Errorf("round trip: %q %v", v, ok)
	}
}
