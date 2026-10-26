//go:build unix

package backupd

import "syscall"

func setUmask(m int) int { return syscall.Umask(m) }
