package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// MigrateHistoryFiles rewrites the update histories of the jails (<state>/updates) and the runs of the automatic image
// upgrades (<state>/image-upgrades) that an earlier release wrote to the current member names: started and finished
// are started_at and finished_at, and the results ok and failed of an update are success and failure. A file is
// replaced whole (temporary file, sync, rename), so a crash leaves the old file or the new one, and a file that is
// current is not touched: running it twice changes nothing.
func MigrateHistoryFiles(stateDir string) error {
	if stateDir == "" {
		return nil
	}
	for _, d := range []struct {
		dir     string
		results bool
	}{{"updates", true}, {"image-upgrades", false}} {
		ents, err := os.ReadDir(filepath.Join(stateDir, d.dir))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		for _, e := range ents {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			if err := migrateHistoryFile(filepath.Join(stateDir, d.dir, e.Name()), d.results); err != nil {
				return err
			}
		}
	}
	return nil
}

func migrateHistoryFile(path string, results bool) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var recs []map[string]any
	if json.Unmarshal(b, &recs) != nil {
		return nil // not a history: left as it is
	}
	changed := false
	for _, r := range recs {
		for _, k := range [][2]string{{"started", "started_at"}, {"finished", "finished_at"}} {
			if v, ok := r[k[0]]; ok {
				delete(r, k[0])
				if _, done := r[k[1]]; !done {
					r[k[1]] = v
				}
				changed = true
			}
		}
		if results {
			switch r["result"] {
			case "ok":
				r["result"], changed = "success", true
			case "failed":
				r["result"], changed = "failure", true
			}
		}
	}
	if !changed {
		return nil
	}
	out, err := json.Marshal(recs)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}
	if _, err := f.Write(out); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
