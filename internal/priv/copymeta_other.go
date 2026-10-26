//go:build !linux && !freebsd

package priv

import (
	"errors"
	"os"
	"time"
)

const platformHasStat = false

type fileStat struct {
	Dev, Ino, Nlink uint64
	UID, GID        uint32
	Atime           time.Time
	Flags           uint32
}

func (s fileStat) id() fileID { return fileID{s.Dev, s.Ino} }

func statOf(os.FileInfo) (fileStat, bool) { return fileStat{}, false }

func setLinkTimes(*os.File, string, time.Time, time.Time) error { return errors.ErrUnsupported }

func copyXattrs(src, dst *os.File) int { return 0 }

func copyFlags(*os.File, uint32) error { return nil }
