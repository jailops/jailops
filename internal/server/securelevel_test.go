package server_test

import (
	"strings"
	"testing"
)

// A FreeBSD jail that sets no securelevel runs at 2; one it sets wins, and -1 leaves the host's level (no line).
func TestJailSecureLevel(t *testing.T) {
	e := seeded(t)
	c := ctxT(t)
	mustJail(t, e, "web")
	conf := func() string {
		t.Helper()
		op, err := e.Admin.Jails().Start(c, "web")
		if err == nil {
			_, err = op.Wait(c)
		}
		if err != nil {
			t.Fatalf("start: %v\n%s", err, e.Log())
		}
		s := e.File("/etc/jailops/jails/alpha_web/jail.conf")
		if op, err = e.Admin.Jails().Stop(c, "web"); err == nil {
			_, err = op.Wait(c)
		}
		if err != nil {
			t.Fatalf("stop: %v", err)
		}
		return s
	}
	set := func(v string) {
		t.Helper()
		op, err := e.Admin.Jails().Config(c, "web", map[string]string{"securelevel": v}, nil, false)
		if err == nil {
			_, err = op.Wait(c)
		}
		if err != nil {
			t.Fatalf("securelevel %s: %v", v, err)
		}
	}
	if s := conf(); !strings.Contains(s, "\tsecurelevel = 2;\n") {
		t.Fatalf("the default is not 2:\n%s", s)
	}
	set("0")
	if s := conf(); !strings.Contains(s, "\tsecurelevel = 0;\n") {
		t.Fatalf("an explicit 0 does not win:\n%s", s)
	}
	set("-1")
	if s := conf(); strings.Contains(s, "securelevel") {
		t.Fatalf("-1 must leave the host's level:\n%s", s)
	}
	for _, bad := range []string{"4", "-2", "two"} {
		if _, err := e.Admin.Jails().Config(c, "web", map[string]string{"securelevel": bad}, nil, false); err == nil {
			t.Errorf("securelevel %s was taken", bad)
		}
	}
}
