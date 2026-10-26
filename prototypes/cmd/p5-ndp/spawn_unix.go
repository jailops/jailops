//go:build unix

package main

import (
	"os"
	"os/exec"
	"syscall"
)

func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// spawnHelper starts a copy of this binary in its own session so it survives the death of the
// parent (and of the operator's ssh session). UNVERIFIED on FreeBSD: Setsid semantics.
func spawnHelper(state string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	logf, err := os.OpenFile(state+".log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "--watchdog-helper", "--state", state)
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
