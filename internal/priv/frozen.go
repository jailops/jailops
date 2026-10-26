package priv

import (
	"path/filepath"
	"strings"
)

// freeze marks dir as in use by a running host command. Until the returned function is called, the file operations
// of the helper refuse to change anything in it: what a command was checked against must be what it reads.
func (l *Local) freeze(dir string) func() {
	dir = filepath.Clean(dir)
	l.frozenMu.Lock()
	if l.frozen == nil {
		l.frozen = map[string]int{}
	}
	l.frozen[dir]++
	l.frozenMu.Unlock()
	return func() {
		l.frozenMu.Lock()
		if l.frozen[dir]--; l.frozen[dir] <= 0 {
			delete(l.frozen, dir)
		}
		l.frozenMu.Unlock()
	}
}

// checkFrozen refuses a change to a path in (or above) a frozen directory.
func (l *Local) checkFrozen(paths ...string) error {
	l.frozenMu.Lock()
	defer l.frozenMu.Unlock()
	for _, p := range paths {
		p = filepath.Clean(p)
		for d := range l.frozen {
			if within(d, p) || strings.HasPrefix(d, strings.TrimSuffix(p, "/")+"/") {
				return forbiddenBusy("%s is in use by a running command", p)
			}
		}
	}
	return nil
}

// reposDirOf returns the REPOS_DIR a pkg command line names, or "".
func reposDirOf(args []string) string {
	for i, a := range args {
		if a == "-o" && i+1 < len(args) {
			if d, ok := strings.CutPrefix(args[i+1], "REPOS_DIR="); ok {
				return d
			}
		}
	}
	return ""
}
