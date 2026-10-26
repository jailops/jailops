//go:build !freebsd

package jailos

import (
	"context"

	"jailops.net/jailops/sdk/api"
)

// KernelObserver is the jail_get(2) reader; it exists only on FreeBSD.
type KernelObserver struct{}

var _ Observer = KernelObserver{}

// NewKernelObserver returns an Observer whose methods fail with CodeUnsupported.
func NewKernelObserver() Observer { return KernelObserver{} }

func unsupported() error {
	return api.Errorf(api.CodeUnsupported, "jail_get(2) is only available on FreeBSD")
}

// List implements Observer.
func (KernelObserver) List(context.Context) ([]Jail, error) { return nil, unsupported() }

// Get implements Observer.
func (KernelObserver) Get(context.Context, string) (Jail, bool, error) {
	return Jail{}, false, unsupported()
}
