//go:build linux

package priv

import "golang.org/x/sys/unix"

func unlockpt(fd int) error { return unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0) }
