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
