package reconcile

import (
	"context"
	"strings"

	"jailops.net/jailops/internal/sys/ifconfig"
	"jailops.net/jailops/internal/sys/jailos"
)

// Observed is what the system looks like right now.
type Observed struct {
	Ifaces  map[string]ifconfig.Iface
	Running map[string]jailos.Jail
}

// Observe reads interfaces and running jails.
func Observe(ctx context.Context, n ifconfig.Client, j jailos.Observer) (*Observed, error) {
	o := &Observed{Ifaces: map[string]ifconfig.Iface{}, Running: map[string]jailos.Jail{}}
	if n != nil {
		ifs, err := n.List(ctx)
		if err != nil {
			return nil, err
		}
		for _, i := range ifs {
			o.Ifaces[i.Name] = i
		}
	}
	if j != nil {
		js, err := j.List(ctx)
		if err != nil {
			return nil, err
		}
		for _, x := range js {
			if !x.Dying {
				o.Running[x.Name] = x
			}
		}
	}
	return o, nil
}

// owned reports whether an interface carries the ownership marker of id.
func owned(i ifconfig.Iface, id string) bool {
	return i.Description == "jailops:"+id
}

// ours reports whether the description marks any JailOps object.
func ours(i ifconfig.Iface) bool { return strings.HasPrefix(i.Description, "jailops:") }
