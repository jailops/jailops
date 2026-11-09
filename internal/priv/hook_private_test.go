package priv

import (
	"context"
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

// Every jail port is a private bridge member: jails never exchange frames, they go through the host.
func TestHookEveryJailPortIsPrivate(t *testing.T) {
	h := newHookEnv(t, api.OSFreeBSD, "10.20.0.12", "none")
	if err := h.local.RunHook(context.Background(), PhasePreStart, "alpha_web"); err != nil {
		t.Fatal(err)
	}
	if found, got := memberPrivate(t, h); !found || !got {
		t.Errorf("member found=%v private=%v", found, got)
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
