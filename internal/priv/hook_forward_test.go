package priv

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"jailops.net/jailops/sdk/api"
)

// forwardAnchor is the rendered anchor of the jail "web" with the IPv4 forward of one host port: the shape the renderer
// writes on the uplink of a network with NAT (the forward and the pass that belongs to it) next to the pass on the
// jail's own link.
func (h *hookEnv) forwardAnchor(hostPort, jailPort string) string {
	return "pass in quick on " + h.host + " all\n" +
		"match in on em0 inet proto tcp from any to (em0) port " + hostPort + " rdr-to 10.20.0.12 port " + jailPort + "\n" +
		"pass in quick on em0 inet proto tcp from any to 10.20.0.12 port " + jailPort + "\n"
}

// exposeStored rewrites the stored record of the jail "web" with the exposure entries the administrator stored.
func (h *hookEnv) exposeStored(t *testing.T, entries ...api.ExposeEntry) {
	t.Helper()
	mp := filepath.Join(h.cfg.MountBase, "projects", "alpha", "jails", "web")
	spec := api.JailSpec{Image: "freebsd-15-0", OS: api.OSFreeBSD, Expose: entries,
		Network: []api.NetworkAttach{{Network: "lan", IPv4: "10.20.0.12", IPv6: "fd00:20::12"}}}
	rec, _ := json.Marshal(map[string]any{"version": 1, "metadata": api.ObjectMeta{Name: "web", Project: "alpha", UUID: hookUUID}, "spec": spec})
	mustWrite(t, filepath.Join(mp, "spec.json"), string(rec))
}

// The start hook runs as root from jail(8) and carries no caller. The forward it loads was admitted when a global
// administrator stored the exposure, so a jail that exposes port 80 over IPv4 starts, restarts and comes up at boot;
// the hook loads only the forwards its own stored record lists.
func TestStartHookLoadsTheForwardsOfTheStoredExposure(t *testing.T) {
	for name, tc := range map[string]struct {
		expose   []api.ExposeEntry
		hostPort string
		jailPort string
		accept   bool
	}{
		"port 80 exposed":                           {[]api.ExposeEntry{{Ports: []string{"80"}}}, "80", "80", true},
		"port 443 exposed with 80":                  {[]api.ExposeEntry{{Ports: []string{"80", "443"}}}, "443", "443", true},
		"a privileged host port for a high port":    {[]api.ExposeEntry{{Ports: []string{"8080"}, HostPort: 80}}, "80", "8080", true},
		"a high port":                               {[]api.ExposeEntry{{Ports: []string{"8080"}}}, "8080", "8080", true},
		"udp is not tcp":                            {[]api.ExposeEntry{{Proto: "udp", Ports: []string{"53"}}}, "53", "53", false},
		"an anchor that forwards a port not listed": {[]api.ExposeEntry{{Ports: []string{"80"}}}, "22", "22", false},
		"the host port the entry does not name":     {[]api.ExposeEntry{{Ports: []string{"8080"}, HostPort: 8081}}, "80", "8080", false},
		"IPv6 only exposure leaves no forward":      {[]api.ExposeEntry{{Ports: []string{"80"}, Families: []string{"6"}}}, "80", "80", false},
		"no exposure at all":                        {nil, "80", "80", false},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHookEnv(t, api.OSFreeBSD, "10.20.0.12", "fd00:20::12")
			h.exposeStored(t, tc.expose...)
			mustWrite(t, h.cfg.PFAnchorFile("alpha_web"), h.forwardAnchor(tc.hostPort, tc.jailPort))
			err := h.local.RunHook(context.Background(), PhasePreStart, "alpha_web")
			switch {
			case tc.accept && err != nil:
				t.Fatalf("prestart refused: %v", err)
			case !tc.accept && err == nil:
				t.Fatal("prestart loaded a forward that the stored exposure does not list")
			case !tc.accept && !api.IsCode(err, api.CodeForbidden):
				t.Fatalf("refused with the wrong error: %v", err)
			}
			if tc.accept && h.pf.LoadCalls["jailops/alpha/web"] != 1 {
				t.Errorf("the anchor was not loaded: %v", h.pf.LoadCalls)
			}
			if !tc.accept && h.pf.LoadCalls["jailops/alpha/web"] != 0 {
				t.Errorf("the anchor was loaded: %v", h.pf.LoadCalls)
			}
		})
	}
}

// Every way a jail with an exposure of a privileged port comes up or goes down without an API caller: the hook phases of
// a start, a restart (stop, then start) and a boot, and the load the reconcile loop makes for the host administrator
// (the system actor carries the assertion). The same load from a caller without the assertion stays refused: a project
// administrator cannot forward a privileged port, with or without a stored exposure.
func TestLowPortExposureLoadsOnEveryEntryPoint(t *testing.T) {
	h := newHookEnv(t, api.OSFreeBSD, "10.20.0.12", "fd00:20::12")
	h.exposeStored(t, api.ExposeEntry{Ports: []string{"80"}})
	anchor := h.forwardAnchor("80", "80")
	mustWrite(t, h.cfg.PFAnchorFile("alpha_web"), anchor)
	ctx := context.Background()
	l := h.local

	for i, phase := range []string{PhasePreStart, PhasePostStart, PhasePostStop, PhasePreStart, PhasePostStart} {
		if err := l.RunHook(ctx, phase, "alpha_web"); err != nil {
			t.Fatalf("step %d, hook %s: %v", i, phase, err)
		}
	}
	// the reconcile loop and the boot pass act for the host administrator
	if err := l.PF().Load(WithAdmin(ctx, "system"), "jailops/alpha/web", anchor); err != nil {
		t.Errorf("the reconcile loop's load: %v", err)
	}
	// an API caller without the assertion: refused, whatever the stored exposure says
	if err := l.PF().Load(ctx, "jailops/alpha/web", anchor); !api.IsCode(err, api.CodeForbidden) {
		t.Errorf("a load without the assertion was not refused: %v", err)
	}
	// and a hook that loads an anchor for a jail whose record lists no exposure is refused
	h.exposeStored(t)
	if err := l.RunHook(ctx, PhasePreStart, "alpha_web"); !api.IsCode(err, api.CodeForbidden) {
		t.Errorf("a forward without a stored exposure was loaded by the hook: %v", err)
	}
}
