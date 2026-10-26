//go:build !freebsd

package jailapi

import "errors"

var errNotFreeBSD = errors.New("jail syscalls exist only on FreeBSD")

// Set is unavailable off FreeBSD.
func Set(ps []Param, flags int) (int, error) { return -1, errNotFreeBSD }

// Get is unavailable off FreeBSD.
func Get(ps []Param, flags int) (int, error) { return -1, errNotFreeBSD }

// Remove is unavailable off FreeBSD.
func Remove(jid int) error { return errNotFreeBSD }
