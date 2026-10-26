package main

import (
	"sort"
	"strings"
)

// chflags(2) flag bits. UNVERIFIED: these are the standard BSD values (unchanged since 4.4BSD
// and documented in chflags(2)); golang.org/x/sys/unix does not export them as named constants
// for FreeBSD, so they are hardcoded here rather than guessed per-build.
const (
	ufNoDump    = 0x00000001
	ufImmutable = 0x00000002
	ufAppend    = 0x00000004
	sfArchived  = 0x00010000
	sfImmutable = 0x00020000
	sfAppend    = 0x00040000
)

var flagBits = []struct {
	name string
	bit  uint32
}{
	{"nodump", ufNoDump},
	{"uchg", ufImmutable},
	{"uappnd", ufAppend},
	{"arch", sfArchived},
	{"schg", sfImmutable},
	{"sappnd", sfAppend},
}

// flagBitOf maps a user-facing flag name (as used by chflags(1) and prototype 9's op generator)
// to its bit.
func flagBitOf(name string) (uint32, bool) {
	for _, f := range flagBits {
		if f.name == name {
			return f.bit, true
		}
	}
	return 0, false
}

// decodeFlags renders a flags bitmask as a sorted, comma-joined list of names (the
// representation capture.Entry.Flags stores, so two reads compare with plain string equality).
func decodeFlags(bits uint32) string {
	var names []string
	for _, f := range flagBits {
		if bits&f.bit != 0 {
			names = append(names, f.name)
		}
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}
