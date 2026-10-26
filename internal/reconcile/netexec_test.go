package reconcile_test

import (
	"strings"
	"testing"

	"jailops.net/jailops/internal/reconcile"
	"jailops.net/jailops/internal/render"
)

func TestJailStepArgvUsesTheHostForms(t *testing.T) {
	step := func(op render.Op, arg string) render.Step {
		return render.Step{Op: op, Iface: "joe1b", Arg: arg, Phase: render.PhaseJail}
	}
	for _, c := range []struct {
		st   render.Step
		want string
	}{
		{step(render.OpAddAddr, "10.0.0.5/24"), "ifconfig -j jo_web joe1b inet 10.0.0.5/24 up"},
		{step(render.OpAddAddr, "fd00::5/64"), "ifconfig -j jo_web joe1b inet6 fd00::5 prefixlen 64 alias"},
		{step(render.OpRemoveAddr, "10.0.0.5/24"), "ifconfig -j jo_web joe1b inet 10.0.0.5/24 -alias"},
		{step(render.OpIfaceUp, ""), "ifconfig -j jo_web joe1b up"},
		{step(render.OpAddDefaultRte, "10.0.0.1"), "route -j jo_web add default 10.0.0.1"},
		{step(render.OpAddDefaultRte, "fe80::1%joe1b"), "route -j jo_web add -inet6 default fe80::1%joe1b"},
		{step(render.OpDelDefaultRte, "10.0.0.1"), "route -j jo_web delete default"},
	} {
		argv, err := reconcile.JailStepArgv(c.st, "jo_web")
		if err != nil || strings.Join(argv, " ") != c.want {
			t.Errorf("%v: %v %v", c.st, argv, err)
		}
	}
	if _, err := reconcile.JailStepArgv(step(render.OpAddAddr, "nope"), "jo_web"); err == nil {
		t.Error("bad address accepted")
	}
}
