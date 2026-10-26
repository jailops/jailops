//go:build linux

package priv

import "syscall"

func setXattr(path, name, val string) error { return syscall.Setxattr(path, name, []byte(val), 0) }

func getXattr(path, name string) (string, error) {
	buf := make([]byte, 64)
	n, err := syscall.Getxattr(path, name, buf)
	return string(buf[:max(n, 0)]), err
}
