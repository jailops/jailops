//go:build !freebsd

package jailapi

// Consts is empty off FreeBSD.
func Consts() map[string]uint64 { return map[string]uint64{} }
