//go:build !linux

package priv

import "errors"

func setXattr(path, name, val string) error { return errors.New("xattr test helper: linux only") }

func getXattr(path, name string) (string, error) { return "", errors.New("linux only") }
