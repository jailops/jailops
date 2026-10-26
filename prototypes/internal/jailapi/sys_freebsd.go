//go:build freebsd

package jailapi

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/unix"
)

func iovecs(ps []Param) ([]unix.Iovec, [][]byte) {
	iov := make([]unix.Iovec, 0, 2*len(ps))
	keep := make([][]byte, 0, 2*len(ps))
	for _, p := range ps {
		name := append([]byte(p.Name), 0)
		keep = append(keep, name)
		var a, b unix.Iovec
		a.Base = &name[0]
		a.SetLen(len(name))
		if len(p.Val) > 0 {
			b.Base = &p.Val[0]
			b.SetLen(len(p.Val))
		}
		iov = append(iov, a, b)
	}
	return iov, keep
}

func call(trap uintptr, ps []Param, flags int) (int, error) {
	iov, keep := iovecs(ps)
	var p unsafe.Pointer
	if len(iov) > 0 {
		p = unsafe.Pointer(&iov[0])
	}
	r, _, e := unix.Syscall(trap, uintptr(p), uintptr(len(iov)), uintptr(flags))
	_ = keep
	if e != 0 {
		return -1, e
	}
	return int(r), nil
}

// errmsg extracts the kernel's text from an "errmsg" output parameter, when present.
func errmsg(ps []Param) string {
	if p, ok := Find(ps, "errmsg"); ok {
		return CString(p.Val)
	}
	return ""
}

// Set calls jail_set(2). It returns the jid. The caller should include ErrBuf() to learn why a call failed.
func Set(ps []Param, flags int) (int, error) {
	// UNVERIFIED: SYS_JAIL_SET from x/sys is usable via unix.Syscall with an iovec array.
	jid, err := call(unix.SYS_JAIL_SET, ps, flags)
	if err != nil {
		if m := errmsg(ps); m != "" {
			return -1, fmt.Errorf("jail_set: %w (kernel: %s)", err, m)
		}
		return -1, fmt.Errorf("jail_set: %w", err)
	}
	return jid, nil
}

// Get calls jail_get(2). Output buffers in ps are filled in place.
func Get(ps []Param, flags int) (int, error) {
	// UNVERIFIED: SYS_JAIL_GET as above.
	jid, err := call(unix.SYS_JAIL_GET, ps, flags)
	if err != nil {
		if m := errmsg(ps); m != "" {
			return -1, fmt.Errorf("jail_get: %w (kernel: %s)", err, m)
		}
		return -1, fmt.Errorf("jail_get: %w", err)
	}
	return jid, nil
}

// Remove calls jail_remove(2).
func Remove(jid int) error {
	_, _, e := unix.Syscall(unix.SYS_JAIL_REMOVE, uintptr(jid), 0, 0)
	if e != 0 {
		return fmt.Errorf("jail_remove(%d): %w", jid, e)
	}
	return nil
}
