package priv

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"jailops.net/jailops/internal/service"
	"jailops.net/jailops/sdk/api"
)

func memberPrivate(t *testing.T, h *hookEnv) (found, private bool) {
	t.Helper()
	list, _ := h.net.List(context.Background())
	for _, i := range list {
		for _, m := range i.Members {
			if m.Name == h.host {
				return true, m.Private
			}
		}
	}
	return false, false
}

// Every jail port is private whatever the network says: jails never exchange frames, they go through the host.
func TestHookEveryJailPortIsPrivate(t *testing.T) {
	for _, private := range []bool{false, true} {
		h := newHookEnv(t, api.OSFreeBSD, "10.20.0.12", "none")
		p := filepath.Join(h.cfg.StateDir, "objects", "Network", "aaaa-1111-2222.json")
		b, _ := os.ReadFile(p)
		var doc map[string]any
		json.Unmarshal(b, &doc)
		doc["spec"].(map[string]any)["private"] = private
		b, _ = json.Marshal(doc)
		os.WriteFile(p, b, 0o644)
		if err := h.local.RunHook(context.Background(), PhasePreStart, "alpha_web"); err != nil {
			t.Fatal(err)
		}
		found, got := memberPrivate(t, h)
		if !found || !got {
			t.Errorf("private=%v: member found=%v private=%v", private, found, got)
		}
	}
}

// localExecer and localRegistrar are copies of the interfaces the service
// layer and the daemon assert on the helper; both implementations must
// satisfy them (the real service.JailExecer is asserted in exec_types.go).
type localExecer interface {
	Exec(ctx context.Context, jail string, argv []string, o ExecOpts) (ExecSession, error)
}

type localRegistrar interface {
	RegisterStorage(ctx context.Context, dataset string) error
	UnregisterStorage(ctx context.Context, dataset string) error
}

func TestHelperSatisfiesTheServiceInterfaces(t *testing.T) {
	var h Host = (*Local)(nil)
	var c Host = (*Client)(nil)
	for _, x := range []Host{h, c} {
		if _, ok := any(x).(localExecer); !ok {
			t.Errorf("%T lacks Exec", x)
		}
		if _, ok := any(x).(localRegistrar); !ok {
			t.Errorf("%T lacks RegisterStorage/UnregisterStorage", x)
		}
		if _, ok := any(x).(service.JailExecer); !ok {
			t.Errorf("%T is not a service.JailExecer", x)
		}
		if _, ok := any(x).(service.StorageRegistrar); !ok {
			t.Errorf("%T is not a service.StorageRegistrar", x)
		}
	}
	// the exec types are the service's own, not look-alikes
	var _ service.ExecOpts = ExecOpts{}
	var _ service.ExecSession = (ExecSession)(nil)
}
