package ndproxy

import (
	"context"
	"errors"
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
)

type fakeExec struct {
	calls []string
	reply func(call string) ([]byte, error)
}

func (f *fakeExec) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	c := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, c)
	if f.reply != nil {
		return f.reply(c)
	}
	return nil, nil
}

func TestRouteArgv(t *testing.T) {
	ex := &fakeExec{}
	rm := RouteManager{Exec: ex}
	ctx := context.Background()
	if err := rm.Add(ctx, jailA, "jo0"); err != nil {
		t.Fatal(err)
	}
	if err := rm.Remove(ctx, jailA, "jo0"); err != nil {
		t.Fatal(err)
	}
	want := []string{"route add -inet6 2001:db8::1:2/128 -interface jo0", "route delete -inet6 2001:db8::1:2/128 -interface jo0"}
	if strings.Join(ex.calls, "|") != strings.Join(want, "|") {
		t.Fatalf("%q", ex.calls)
	}
	for _, bad := range []string{"", "-x", "jo0; rm", "a b"} {
		if err := rm.Add(ctx, jailA, bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestRouteExistsAndGone(t *testing.T) {
	ex := &fakeExec{reply: func(c string) ([]byte, error) {
		switch {
		case strings.HasPrefix(c, "route add"):
			return []byte("route: writing to routing socket: File exists"), errors.New("exit 1")
		case strings.HasPrefix(c, "route delete"):
			return []byte("route: writing to routing socket: not in table"), errors.New("exit 1")
		}
		return nil, nil
	}}
	rm := RouteManager{Exec: ex}
	if err := rm.Add(context.Background(), jailA, "jo0"); err != nil || len(ex.calls) != 2 || !strings.HasPrefix(ex.calls[1], "route change") {
		t.Fatal(err, ex.calls)
	}
	if err := rm.Remove(context.Background(), jailA, "jo0"); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureUplinkNotMember(t *testing.T) {
	members := "jo0: flags=1008843<UP>\n\tmember: epair0a flags=143<LEARNING>\n\tmember: em0 flags=143<LEARNING>\n"
	ex := &fakeExec{reply: func(c string) ([]byte, error) {
		if c == "ifconfig -g bridge" {
			return []byte("jo0 jo1\n"), nil
		}
		if c == "ifconfig jo0" {
			return []byte(members), nil
		}
		return []byte("jo1: flags=1\n"), nil
	}}
	rm := RouteManager{Exec: ex}
	err := rm.EnsureUplinkNotMember(context.Background(), "em0")
	if !errors.Is(err, &api.Error{Code: api.CodeConflict}) {
		t.Fatal(err)
	}
	if err := rm.EnsureUplinkNotMember(context.Background(), "igb0"); err != nil {
		t.Fatal(err)
	}
	for _, c := range ex.calls {
		if strings.Contains(c, "deletem") {
			t.Fatal("modified a bridge")
		}
	}
}
