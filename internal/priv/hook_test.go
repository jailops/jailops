package priv

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"jailops.net/jailops/internal/ipam"
	"jailops.net/jailops/internal/sys/run"
	"jailops.net/jailops/internal/sys/zfs"
	"jailops.net/jailops/sdk/api"
)

const hookUUID = "5f0a1c3e-9a7b-4c1d-8e2f-0123456789ab"

type hookEnv struct {
	*env
	host, jail string
	// jailErr, when set, is the stderr of every host-side ifconfig -j and
	// route -j the hook runs.
	jailErr string
}

// newHookEnv lays out one jail "web" attached to network "lan" on bridge jo0.
func newHookEnv(t *testing.T, os api.OS, ipv4, ipv6 string) *hookEnv {
	t.Helper()
	e := newEnv(t)
	ctx := context.Background()
	h := &hookEnv{env: e}
	h.host, h.jail, _ = ipam.EpairNames(hookUUID, 0)

	mp := filepath.Join(e.cfg.MountBase, "projects", "alpha", "jails", "web")
	e.managedDS("tank/jailops/projects")
	e.managedDS("tank/jailops/projects/alpha")
	e.managedDS("tank/jailops/projects/alpha/jails")
	if err := e.zfs.Create(ctx, zfs.CreateOpts{Name: "tank/jailops/projects/alpha/jails/web", Props: map[string]string{"jailops:managed": "on", "mountpoint": mp}}); err != nil {
		t.Fatal(err)
	}
	spec := api.JailSpec{Image: "freebsd-15.0@stable", OS: os, Network: []api.NetworkAttach{{Network: "lan", IPv4: ipv4, IPv6: ipv6}}}
	rec, _ := json.Marshal(map[string]any{"version": 1, "metadata": api.ObjectMeta{Name: "web", Project: "alpha", UUID: hookUUID}, "spec": spec})
	mustWrite(t, filepath.Join(mp, "spec.json"), string(rec))

	nw := api.NetworkSpec{Bridge: "jo0", Uplink: "em0", MTU: 1400,
		IPv4: &api.IPv4Net{Subnet: "10.20.0.0/24", Gateway: "10.20.0.1", Range: "10.20.0.10-10.20.0.200"},
		IPv6: &api.IPv6Net{Mode: api.IPv6Routed, Subnet: "fd00:20::/64", Gateway: "fd00:20::1", Range: "fd00:20::10-fd00:20::ff"}}
	doc, _ := json.Marshal(map[string]any{"apiVersion": "jailops/v1", "kind": "Network", "metadata": api.ObjectMeta{Name: "lan", UUID: "aaaa-1111-2222"}, "spec": nw})
	mustWrite(t, filepath.Join(e.cfg.StateDir, "objects", "Network", "aaaa-1111-2222.json"), string(doc))
	// a second network with another name must not be picked
	other, _ := json.Marshal(map[string]any{"kind": "Network", "metadata": api.ObjectMeta{Name: "dmz", UUID: "bbbb-1111-2222"}, "spec": api.NetworkSpec{Bridge: "jo9"}})
	mustWrite(t, filepath.Join(e.cfg.StateDir, "objects", "Network", "bbbb-1111-2222.json"), string(other))

	mustWrite(t, filepath.Join(e.cfg.ConfDir, "alpha_web.conf"), e.goodConf())
	if err := e.net.BridgeCreate(ctx, "jo0"); err != nil {
		t.Fatal(err)
	}
	e.runner.Add(run.Rule{Name: "ifconfig", Args: []string{run.RestArgs}})
	for _, bin := range []string{"/sbin/ifconfig", "/sbin/route"} {
		bin := bin
		e.runner.Add(run.Rule{Name: bin, Args: []string{run.RestArgs}, Fn: func(c run.Cmd) (run.Result, error) {
			if h.jailErr != "" {
				return run.Result{ExitCode: 1}, &run.ExitError{Name: bin, Args: c.Args, Code: 1, Stderr: h.jailErr}
			}
			return run.Result{}, nil
		}})
	}
	return h
}

func (h *hookEnv) ifaces() map[string][]string {
	list, _ := h.net.List(context.Background())
	m := map[string][]string{}
	for _, i := range list {
		var mem []string
		for _, x := range i.Members {
			mem = append(mem, x.Name)
		}
		m[i.Name] = mem
	}
	return m
}

func (h *hookEnv) ifconfigCalls() []string {
	var out []string
	for _, c := range h.runner.Calls() {
		if c.Cmd.Name == "ifconfig" {
			out = append(out, strings.Join(c.Cmd.Args, " "))
		}
	}
	return out
}

func TestHookLifecycleFreeBSD(t *testing.T) {
	h := newHookEnv(t, api.OSFreeBSD, "10.20.0.12", "fd00:20::12")
	ctx := context.Background()
	l := h.local

	if err := l.RunHook(ctx, PhasePreStart, "alpha_web"); err != nil {
		t.Fatalf("prestart: %v", err)
	}
	ifs := h.ifaces()
	if _, ok := ifs[h.host]; !ok {
		t.Fatalf("host epair end %s missing: %v", h.host, ifs)
	}
	if _, ok := ifs[h.jail]; !ok {
		t.Fatalf("jail epair end %s missing before start", h.jail)
	}
	if len(ifs["jo0"]) != 1 || ifs["jo0"][0] != h.host {
		t.Errorf("bridge members %v", ifs["jo0"])
	}
	list, _ := h.net.List(ctx)
	for _, i := range list {
		if i.Name == h.host && (i.Description != "jailops:"+hookUUID || !i.Up()) {
			t.Errorf("host end %+v", i)
		}
	}
	// the host holds permanent entries for the jail's addresses on the gateway interface before the jail starts
	if nb := h.net.Neighbors(); len(nb) != 3 || nb["jo0gb fd00:20::12"] == "" || nb["jo0gb 10.20.0.12"] == "" {
		t.Errorf("neighbor entries after prestart: %v", nb)
	}
	calls := h.ifconfigCalls()
	if len(calls) != 6 || !strings.Contains(calls[0], h.host+" ether ") || !strings.Contains(calls[1], h.jail+" ether ") ||
		calls[2] != h.host+" mtu 1400" || calls[3] != h.jail+" mtu 1400" ||
		calls[4][:len("jo0 static "+h.host+" ")] != "jo0 static "+h.host+" " || calls[5] != "jo0gb staticarp" {
		t.Errorf("ifconfig calls %q", calls)
	}

	// idempotent
	before := h.ifaces()
	if err := l.RunHook(ctx, PhasePreStart, "alpha_web"); err != nil {
		t.Fatalf("second prestart: %v", err)
	}
	if got := h.ifaces(); len(got) != len(before) || len(got["jo0"]) != 1 {
		t.Errorf("state changed on the second run: %v vs %v", got, before)
	}

	// the jail starts: jail.conf's vnet.interface moves the jail end
	if err := h.jails.Create(ctx, filepath.Join(h.cfg.ConfDir, "alpha_web.conf"), "alpha_web"); err != nil {
		t.Fatal(err)
	}
	// the rendered anchor is left by the reconcile loop
	mustWrite(t, h.cfg.PFAnchorFile("alpha_web"), "pass in on "+h.host+" all\n")
	if err := l.RunHook(ctx, PhasePostStart, "alpha_web"); err != nil {
		t.Fatalf("poststart: %v", err)
	}
	if _, ok := h.ifaces()[h.jail]; ok {
		t.Error("the jail end was not moved into the jail")
	}
	if h.pf.LoadCalls["jailops/alpha/web"] != 1 {
		t.Errorf("anchor loads: %v", h.pf.LoadCalls)
	}
	for _, c := range h.jails.Calls() {
		if c.Op == "exec" {
			t.Errorf("a FreeBSD jail configures itself, but the hook ran %v", c.Argv)
		}
	}
	if err := l.RunHook(ctx, PhasePostStart, "alpha_web"); err != nil {
		t.Fatalf("second poststart: %v", err)
	}

	if err := l.RunHook(ctx, PhasePreStop, "alpha_web"); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.ifaces()[h.host]; !ok {
		t.Error("prestop removed the interface while the jail still runs")
	}

	// stop: the kernel removes the jail, poststop undoes the host side
	if err := h.jails.Remove(ctx, filepath.Join(h.cfg.ConfDir, "alpha_web.conf"), "alpha_web"); err != nil {
		t.Fatal(err)
	}
	if err := l.RunHook(ctx, PhasePostStop, "alpha_web"); err != nil {
		t.Fatalf("poststop: %v", err)
	}
	ifs = h.ifaces()
	if _, ok := ifs[h.host]; ok {
		t.Error("host epair end survived poststop")
	}
	if len(ifs["jo0"]) != 0 {
		t.Errorf("bridge members after poststop: %v", ifs["jo0"])
	}
	if _, ok := ifs["jo0"]; !ok {
		t.Error("poststop removed the bridge")
	}
	if a, _ := h.pf.Read(ctx, "jailops/alpha/web"); a != "" {
		t.Errorf("anchor still loaded: %q", a)
	}
	if nb := h.net.Neighbors(); len(nb) != 0 {
		t.Errorf("neighbor entries survived poststop: %v", nb)
	}
	if err := l.RunHook(ctx, PhasePostStop, "alpha_web"); err != nil {
		t.Errorf("second poststop: %v", err)
	}
	// the whole cycle again
	if err := l.RunHook(ctx, PhasePreStart, "alpha_web"); err != nil {
		t.Errorf("restart: %v", err)
	}
}

func TestHookConfiguresLinuxJailFromTheHost(t *testing.T) {
	h := newHookEnv(t, api.OSLinux, "10.20.0.12", "fd00:20::12")
	ctx := context.Background()
	if err := h.local.RunHook(ctx, PhasePreStart, "alpha_web"); err != nil {
		t.Fatal(err)
	}
	if err := h.jails.Create(ctx, filepath.Join(h.cfg.ConfDir, "alpha_web.conf"), "alpha_web"); err != nil {
		t.Fatal(err)
	}
	if err := h.local.RunHook(ctx, PhasePostStart, "alpha_web"); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range h.runner.Calls() {
		if c.Cmd.Name == "/sbin/ifconfig" || c.Cmd.Name == "/sbin/route" {
			got = append(got, c.Cmd.Name+" "+strings.Join(c.Cmd.Args, " "))
		}
	}
	want := []string{
		"/sbin/ifconfig -j alpha_web " + h.jail + " inet 10.20.0.12/32 up",
		"/sbin/ifconfig -j alpha_web " + h.jail + " inet6 fd00:20::12 prefixlen 128",
		"/sbin/ifconfig -j alpha_web " + h.jail + " up",
		"/sbin/route -j alpha_web add -host 10.20.0.1 -interface " + h.jail,
		"/sbin/route -j alpha_web add default 10.20.0.1",
		"/sbin/route -j alpha_web add -inet6 default fe80::1%" + h.jail,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("host-side commands:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, c := range h.jails.Calls() {
		if c.Op == "exec" {
			t.Errorf("a command ran inside the jail: %v", c.Argv)
		}
	}
	// running poststart again is safe
	if err := h.local.RunHook(ctx, PhasePostStart, "alpha_web"); err != nil {
		t.Errorf("second poststart: %v", err)
	}
}

func TestHookAlreadyExistsInJailIsTolerated(t *testing.T) {
	h := newHookEnv(t, api.OSLinux, "10.20.0.12", "none")
	ctx := context.Background()
	h.local.RunHook(ctx, PhasePreStart, "alpha_web")
	h.jails.Create(ctx, filepath.Join(h.cfg.ConfDir, "alpha_web.conf"), "alpha_web")
	h.jailErr = "route: writing to routing socket: File exists"
	if err := h.local.RunHook(ctx, PhasePostStart, "alpha_web"); err != nil {
		t.Errorf("File exists must not fail the hook: %v", err)
	}
	h.jailErr = "ifconfig: interface does not exist"
	if err := h.local.RunHook(ctx, PhasePostStart, "alpha_web"); err == nil {
		t.Error("a real failure inside the jail was swallowed")
	}
}

func TestHookRefusals(t *testing.T) {
	h := newHookEnv(t, api.OSFreeBSD, "10.20.0.12", "none")
	ctx := context.Background()
	l := h.local
	for _, name := range []string{"-x", "", "a b", "a/b", "../web", "web;id"} {
		if err := l.RunHook(ctx, PhasePreStart, name); err == nil {
			t.Errorf("jail %q accepted", name)
		}
	}
	if err := l.RunHook(ctx, "prestartx", "alpha_web"); !api.IsCode(err, api.CodeInvalid) {
		t.Errorf("unknown phase: %v", err)
	}
	if err := l.RunHook(ctx, PhasePreStart, "ghost"); err == nil {
		t.Error("a jail without a dataset")
	}
	// the bridge is the reconcile loop's job; the hook does not create it
	if err := h.net.BridgeDestroy(ctx, "jo0"); err != nil {
		t.Fatal(err)
	}
	if err := l.RunHook(ctx, PhasePreStart, "alpha_web"); !api.IsCode(err, api.CodeNotFound) {
		t.Errorf("missing bridge: %v", err)
	}
	if len(h.ifaces()) != 1 {
		t.Errorf("hook created interfaces without a bridge: %v", h.ifaces())
	}
}

func TestHookSpecProblems(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		ipv4, ipv6 string
		mutate     func(h *hookEnv)
	}{
		"auto address": {ipv4: "auto", ipv6: "none"},
		"bad address":  {ipv4: "10.20.0.999", ipv6: "none"},
		"wrong family": {ipv4: "fd00::1", ipv6: "none"},
		"no network":   {ipv4: "none", ipv6: "none", mutate: func(h *hookEnv) { _ = removeAll(filepath.Join(h.cfg.StateDir, "objects", "Network")) }},
		"spec names other": {ipv4: "none", ipv6: "none", mutate: func(h *hookEnv) {
			mustWrite(t, filepath.Join(h.cfg.MountBase, "projects", "alpha", "jails", "web", "spec.json"), `{"metadata":{"name":"db","project":"alpha","uuid":"`+hookUUID+`"},"spec":{}}`)
		}},
		"no uuid": {ipv4: "none", ipv6: "none", mutate: func(h *hookEnv) {
			mustWrite(t, filepath.Join(h.cfg.MountBase, "projects", "alpha", "jails", "web", "spec.json"), `{"metadata":{"name":"web","project":"alpha"},"spec":{}}`)
		}},
		"no project": {ipv4: "none", ipv6: "none", mutate: func(h *hookEnv) {
			mustWrite(t, filepath.Join(h.cfg.MountBase, "projects", "alpha", "jails", "web", "spec.json"), `{"metadata":{"name":"web","uuid":"`+hookUUID+`"},"spec":{}}`)
		}},
		"corrupt spec": {ipv4: "none", ipv6: "none", mutate: func(h *hookEnv) {
			mustWrite(t, filepath.Join(h.cfg.MountBase, "projects", "alpha", "jails", "web", "spec.json"), `{"metadata":`)
		}},
		"mountpoint outside the roots": {ipv4: "none", ipv6: "none", mutate: func(h *hookEnv) {
			if err := h.zfs.Set(ctx, "tank/jailops/projects/alpha/jails/web", map[string]string{"mountpoint": "/etc"}); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		h := newHookEnv(t, api.OSFreeBSD, tc.ipv4, tc.ipv6)
		if tc.mutate != nil {
			tc.mutate(h)
		}
		if err := h.local.RunHook(ctx, PhasePreStart, "alpha_web"); err == nil {
			t.Errorf("%s: prestart succeeded", name)
		}
		if _, ok := h.ifaces()[h.host]; ok {
			t.Errorf("%s: an epair was created for a broken spec", name)
		}
	}
}

func TestHookUsesTheSpecPreviousCopy(t *testing.T) {
	h := newHookEnv(t, api.OSFreeBSD, "10.20.0.12", "none")
	mp := filepath.Join(h.cfg.MountBase, "projects", "alpha", "jails", "web")
	b, _ := h.local.ReadFile(context.Background(), filepath.Join(mp, "spec.json"))
	mustWrite(t, filepath.Join(mp, "spec.json.previous"), string(b))
	if err := removeAll(filepath.Join(mp, "spec.json")); err != nil {
		t.Fatal(err)
	}
	if err := h.local.RunHook(context.Background(), PhasePreStart, "alpha_web"); err != nil {
		t.Errorf("fallback to spec.json.previous: %v", err)
	}
}

func TestHookPostStopWithoutSpecStillDropsTheAnchor(t *testing.T) {
	h := newHookEnv(t, api.OSFreeBSD, "10.20.0.12", "none")
	ctx := context.Background()
	h.pf.Load(ctx, "jailops/alpha/web", "pass all\n")
	_ = removeAll(filepath.Join(h.cfg.MountBase, "projects", "alpha", "jails", "web", "spec.json"))
	_ = h.local.RunHook(ctx, PhasePostStop, "alpha_web")
	if a, _ := h.pf.Read(ctx, "jailops/alpha/web"); a != "" {
		t.Errorf("anchor survived: %q", a)
	}
}
