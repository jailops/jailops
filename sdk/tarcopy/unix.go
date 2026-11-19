package tarcopy

import (
	"io/fs"
	"os"
	"syscall"
	"time"
)

// owner is the numeric owner and group of a file.
func owner(st fs.FileInfo) (uid, gid int, ok bool) {
	s, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return int(s.Uid), int(s.Gid), true
}

// setTimes sets the access and modification time of the open file to t, on the descriptor, so that a name swapped for a
// symlink in the meantime is not touched.
func setTimes(f *os.File, t time.Time) {
	if t.IsZero() {
		return
	}
	tv := []syscall.Timeval{syscall.NsecToTimeval(t.UnixNano()), syscall.NsecToTimeval(t.UnixNano())}
	_ = syscall.Futimes(int(f.Fd()), tv)
}
