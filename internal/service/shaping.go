package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"jailops.net/jailops/internal/reconcile"
	"jailops.net/jailops/internal/render"
	"jailops.net/jailops/internal/sys/run"
	"jailops.net/jailops/internal/validate"
	"jailops.net/jailops/sdk/api"
)

// DummynetModule is the kernel module whose pipes carry the bandwidth limits.
const DummynetModule = "dummynet"

// shapedDevice is one device of a jail with a bandwidth limit: nic.<device>.egress, in bits per second.
type shapedDevice struct {
	key              render.PipeKey
	egress           uint64
	jail, deviceName string
}

// shapedDevices lists every device of every jail that has a bandwidth limit, from the specs the jails have now (with
// their profiles merged). A limit that does not parse is skipped here; the spec validation refuses it when it is set.
func (s *Service) shapedDevices() []shapedDevice {
	s.mu.RLock()
	names := sortedKeys(s.jails)
	jails := make([]api.Jail, 0, len(names))
	for _, n := range names {
		jails = append(jails, s.jails[n].jail)
	}
	s.mu.RUnlock()
	var out []shapedDevice
	for _, j := range jails {
		for i, nd := range s.effective(j).Network {
			if nd.Egress == "" {
				continue
			}
			bits, err := validate.ParseRate(nd.Egress)
			if err != nil || bits == 0 {
				continue
			}
			out = append(out, shapedDevice{key: render.PipeKey{Jail: j.Metadata.UUID, Device: i}, egress: bits, jail: jailKey(j), deviceName: nd.Name})
		}
	}
	return out
}

// shapingPlan gives the pipe number of every shaped device and the pipes the host must have: one per device, with the
// bandwidth of what the jail sends.
func (s *Service) shapingPlan() (map[render.PipeKey]int, []reconcile.PipeDesired, error) {
	devs := s.shapedDevices()
	keys := make([]render.PipeKey, len(devs))
	for i, d := range devs {
		keys[i] = d.key
	}
	numbers, err := render.PipeNumbers(keys)
	if err != nil {
		return nil, nil, err
	}
	var want []reconcile.PipeDesired
	for _, d := range devs {
		want = append(want, reconcile.PipeDesired{Number: numbers[d.key], Rate: d.egress})
	}
	return numbers, want, nil
}

// hasShaping says whether any device of any jail has a bandwidth limit: only then does the host need dummynet.
func (s *Service) hasShaping() bool { return len(s.shapedDevices()) > 0 }

// ensurePipes loads dummynet and configures the pipes of one jail before its anchor is loaded: a packet sent into a pipe
// that does not exist is lost, and the reconcile loop that keeps all pipes right runs on its own schedule.
func (s *Service) ensurePipes(ctx context.Context, shapes []render.Shape, rates map[int]uint64) error {
	if len(shapes) == 0 || s.d.Run == nil {
		return nil
	}
	if _, err := s.d.Run.Run(ctx, run.Cmd{Name: "kldstat", Args: []string{"-n", DummynetModule}}); err != nil {
		if _, err := s.d.Run.Run(ctx, run.Cmd{Name: "kldload", Args: []string{DummynetModule}}); err != nil {
			return fmt.Errorf("the kernel module %s is not loaded and cannot be loaded, so the bandwidth limits of the jail cannot be enforced: %w", DummynetModule, err)
		}
	}
	for _, sh := range shapes {
		if err := s.configurePipe(ctx, sh.Pipe, rates[sh.Pipe]); err != nil {
			return err
		}
	}
	return nil
}

// configurePipe sets the bandwidth of one pipe: dnctl pipe N config bw RATE ("0" is unlimited).
func (s *Service) configurePipe(ctx context.Context, n int, bits uint64) error {
	bw := "0"
	if bits > 0 {
		bw = render.PipeRate(bits)
	}
	_, err := s.d.Run.Run(ctx, run.Cmd{Name: "dnctl", Args: []string{"pipe", strconv.Itoa(n), "config", "bw", bw}})
	if err != nil {
		return fmt.Errorf("dnctl pipe %d config bw %s: %s", n, bw, strings.TrimSpace(err.Error()))
	}
	return nil
}
