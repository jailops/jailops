//go:build !unix

package backupd

func setUmask(m int) int { return 0 }
