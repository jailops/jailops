package jailos

import (
	"context"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"

	"jailops.net/jailops/sdk/api"
)

// KernelObserver reads running jails with jail_get(2) instead of spawning jls.
//
// UNVERIFIED: this file has never run on FreeBSD. It relies on
// unix.SYS_JAIL_GET being callable from pure Go with an iovec array of
// alternating name and value buffers (name strings NUL terminated, int32 for
// numeric parameters, "lastjid" iteration until ENOENT). Design section 16
// item 1 (prototype 1) settles it.
type KernelObserver struct{}

var _ Observer = KernelObserver{}

// NewKernelObserver returns the jail_get(2) based Observer.
func NewKernelObserver() Observer { return KernelObserver{} }

type iovBuilder struct {
	iov  []unix.Iovec
	keep [][]byte
}

func (b *iovBuilder) add(p []byte) {
	var v unix.Iovec
	if len(p) > 0 {
		v.Base = &p[0]
	}
	v.SetLen(len(p))
	b.iov = append(b.iov, v)
	b.keep = append(b.keep, p)
}

func (b *iovBuilder) name(n string) { b.add(append([]byte(n), 0)) }

func cstr(p []byte) string {
	for i, c := range p {
		if c == 0 {
			return string(p[:i])
		}
	}
	return string(p)
}

func hostint(p []byte) int32 { return *(*int32)(unsafe.Pointer(&p[0])) }

// next returns the first jail with jid greater than last, or ok=false at the end.
func next(last int32) (Jail, bool, error) {
	lastb := make([]byte, 4)
	*(*int32)(unsafe.Pointer(&lastb[0])) = last
	nameb := make([]byte, 256)
	pathb := make([]byte, 1024)
	hostb := make([]byte, 256)
	vnetb := make([]byte, 4)
	dyingb := make([]byte, 4)
	var b iovBuilder
	b.name("lastjid")
	b.add(lastb)
	b.name("name")
	b.add(nameb)
	b.name("path")
	b.add(pathb)
	b.name("host.hostname")
	b.add(hostb)
	b.name("vnet")
	b.add(vnetb)
	b.name("dying")
	b.add(dyingb)
	jid, _, errno := unix.Syscall(unix.SYS_JAIL_GET, uintptr(unsafe.Pointer(&b.iov[0])), uintptr(len(b.iov)), 0)
	runtime.KeepAlive(&b)
	if errno != 0 {
		if errno == unix.ENOENT {
			return Jail{}, false, nil
		}
		return Jail{}, false, api.Errorf(api.CodeInternal, "jail_get: %v", errno)
	}
	return Jail{
		JID: int(jid), Name: cstr(nameb), Path: cstr(pathb), Hostname: cstr(hostb),
		VNet: hostint(vnetb) != 0, Dying: hostint(dyingb) != 0, Params: map[string]string{},
	}, true, nil
}

// List implements Observer.
func (KernelObserver) List(ctx context.Context) ([]Jail, error) {
	var out []Jail
	var last int32
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		j, ok, err := next(last)
		if err != nil {
			return nil, err
		}
		if !ok {
			return out, nil
		}
		out = append(out, j)
		last = int32(j.JID)
	}
}

// Get implements Observer.
func (k KernelObserver) Get(ctx context.Context, name string) (Jail, bool, error) {
	if err := ValidateName(name); err != nil {
		return Jail{}, false, err
	}
	js, err := k.List(ctx)
	if err != nil {
		return Jail{}, false, err
	}
	for _, j := range js {
		if j.Name == name {
			return j, true, nil
		}
	}
	return Jail{}, false, nil
}
