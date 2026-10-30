package apps

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"jailops.net/jailops/sdk/api"
)

// Problem is one invalid file found by Check.
type Problem struct {
	File   string `json:"file"`
	Reason string `json:"reason"`
}

// CheckResult is what Check found under one path.
type CheckResult struct {
	// Definitions is the number of definition files read.
	Definitions int
	// Icons is the number of icon files read.
	Icons    int
	Problems []Problem
}

// Check validates a catalog directory or one definition file with the
// daemon's own rules: a directory is read as the list reads it (Load), and
// the icon of every valid definition as a single read does (Find). It needs
// no daemon and no network. A path that cannot be read, a directory without
// definitions and a file that is not a .yaml are problems too, and so is
// every line that still holds a TODO marker of a starter file (the daemon
// does not look for them, so a draft installs while it is being written).
// File names in the problems are path joined with the file name; reasons are
// one line.
func Check(path string) CheckResult {
	var r CheckResult
	bad := func(file string, err error) {
		r.Problems = append(r.Problems, Problem{File: file, Reason: reason(err)})
	}
	fi, err := os.Stat(path)
	if err != nil {
		bad(path, err)
		return r
	}
	var dir string
	var entries []Entry
	if fi.IsDir() {
		dir = path
		entries, err = Load(dir)
		if err != nil {
			bad(path, err)
			return r
		}
		if len(entries) == 0 {
			bad(path, fmt.Errorf("no definitions (*.yaml) in this directory"))
			return r
		}
	} else {
		var file string
		dir, file = filepath.Split(path)
		if !strings.HasSuffix(file, ".yaml") {
			bad(path, fmt.Errorf("a definition is a .yaml file"))
			return r
		}
		entries = []Entry{load(dir, file)}
	}
	for _, e := range entries {
		r.Definitions++
		if e.Err == nil && e.App.Icon != "" {
			r.Icons++
			_, e.Err = loadIcon(dir, e.App.Icon)
		}
		file := filepath.Join(dir, e.File)
		if e.Err != nil {
			bad(file, e.Err)
		}
		if fi, err := os.Lstat(file); err == nil && fi.Mode().IsRegular() {
			if data, err := readBounded(file, MaxFileBytes); err == nil {
				for _, n := range TodoLines(data) {
					r.Problems = append(r.Problems, Problem{File: file, Reason: fmt.Sprintf("line %d: replace TODO", n)})
				}
			}
		}
	}
	return r
}

// TodoMarker is what 'jailops app new' and 'jailops app export' leave where
// a person has to decide; Check reports every line that holds one.
const TodoMarker = "TODO"

var todoRE = regexp.MustCompile(`\b` + TodoMarker + `\b`)

// TodoLines returns the numbers (from 1) of the lines that hold a TODO marker.
func TodoLines(data []byte) []int {
	var out []int
	for i, l := range strings.Split(string(data), "\n") {
		if todoRE.MatchString(l) {
			out = append(out, i+1)
		}
	}
	return out
}

// reason is an error as one line: the message of a validation error without
// its code, the lines of a YAML error joined.
func reason(err error) string {
	msg := err.Error()
	var ae *api.Error
	if errors.As(err, &ae) {
		msg = ae.Message
	}
	return strings.NewReplacer(":\n  ", ": ", "\n  ", "; ", "\n", "; ").Replace(msg)
}
