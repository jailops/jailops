package harness

import "os"

// RunStdinFile runs argv with the contents of path on stdin (used to feed a saved, possibly
// truncated, zfs stream into `zfs recv`). A dry run only prints.
func (e *Env) RunStdinFile(path string, argv ...string) Result {
	if e.DryRun {
		e.Printf("   (stdin from %s)\n", path)
		return e.run(e.Timeout, nil, argv)
	}
	f, err := os.Open(path)
	if err != nil {
		return Result{Err: err, Code: -1}
	}
	defer f.Close()
	e.Printf("   (stdin from %s)\n", path)
	return e.run(e.Timeout, f, argv)
}
