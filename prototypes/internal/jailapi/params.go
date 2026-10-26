// Package jailapi builds and parses jail(2) parameter lists. The pure parts live here so they
// are unit-tested on Linux; the system calls are in sys_freebsd.go.
//
// Nothing in this package was run on FreeBSD by its author.
package jailapi

import (
	"encoding/binary"
	"fmt"
	"strings"
)

// Flags for jail_set(2) and jail_get(2).
// UNVERIFIED: values copied from memory of sys/jail.h on FreeBSD 14/15; golang.org/x/sys/unix
// defines SYS_JAIL_SET, SYS_JAIL_GET and SYS_JAIL_REMOVE but, as far as the author knows, not these.
const (
	JailCreate = 0x01
	JailUpdate = 0x02
	JailAttach = 0x04
	JailDying  = 0x08
)

// Param is one name/value iovec pair. Val nil with Size 0 is a boolean flag parameter.
type Param struct {
	Name string
	Val  []byte // value bytes as the kernel will see them
	Out  bool   // true for get: Val is an output buffer
}

// Str is a NUL-terminated string parameter.
func Str(name, v string) Param { return Param{Name: name, Val: append([]byte(v), 0)} }

// Int32 is a native-endian int parameter (jid, securelevel, ...).
func Int32(name string, v int32) Param {
	b := make([]byte, 4)
	binary.NativeEndian.PutUint32(b, uint32(v))
	return Param{Name: name, Val: b}
}

// Flag is a boolean parameter given by name only (e.g. "persist").
// UNVERIFIED: the kernel accepts a zero-length value for boolean parameters.
func Flag(name string) Param { return Param{Name: name} }

// Buf is an output buffer for jail_get.
func Buf(name string, size int) Param { return Param{Name: name, Val: make([]byte, size), Out: true} }

// ErrBuf is the "errmsg" output buffer both syscalls fill on failure.
func ErrBuf() Param { return Buf("errmsg", 256) }

// CString returns the bytes up to the first NUL.
func CString(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

// AsInt32 decodes a native-endian int32 value buffer.
func AsInt32(b []byte) (int32, error) {
	if len(b) < 4 {
		return 0, fmt.Errorf("value is %d bytes, want 4", len(b))
	}
	return int32(binary.NativeEndian.Uint32(b)), nil
}

// Find returns the parameter with the given name.
func Find(ps []Param, name string) (Param, bool) {
	for _, p := range ps {
		if p.Name == name {
			return p, true
		}
	}
	return Param{}, false
}

// JLS is one jail as printed by `jls -n`.
type JLS map[string]string

// ParseJLSn parses `jls -n <params>` output: one jail per line, name=value pairs separated by
// spaces. Values containing spaces are not supported (the prototype uses controlled values).
func ParseJLSn(out string) []JLS {
	var res []JLS
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		j := JLS{}
		for _, f := range strings.Fields(line) {
			k, v, _ := strings.Cut(f, "=")
			j[k] = v
		}
		res = append(res, j)
	}
	return res
}

// OwnedName reports whether a jail name is one this prototype may touch.
func OwnedName(n string) bool {
	return strings.HasPrefix(n, "jop1") && len(n) > 4
}
