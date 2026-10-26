//go:build !unix

package main

import "errors"

func alive(int) bool { return false }

func spawnHelper(string) error { return errors.New("watchdog helper needs a unix system") }
