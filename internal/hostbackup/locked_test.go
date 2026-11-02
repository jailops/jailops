package hostbackup

import (
	"strings"
	"testing"
)

// A locked encrypted volume cannot be read: the backup leaves it out, says so in the run, and backs up the rest.
func TestBackupSkipsALockedVolume(t *testing.T) {
	r := newRig(t, rigOpts{remote: mustMem(t)})
	j := r.jail("web1", "uuid-web1", nil)
	locked := false
	for i := range j.Datasets {
		if j.Datasets[i].Kind == KindVolume {
			j.Datasets[i].Skip, locked = SkipLocked, true
		}
	}
	if !locked {
		t.Fatal("the test jail has no volume")
	}
	res := r.backup(j)
	tr := res.Targets[0]
	if tr.Err != nil {
		t.Fatal(tr.Err)
	}
	for _, st := range tr.Result.Streams {
		if strings.HasPrefix(st.Name, "data/") {
			t.Fatalf("the locked volume was captured: %+v", st)
		}
	}
	if len(tr.Result.Streams) != 3 { // ".", etc, local
		t.Fatalf("streams %+v", tr.Result.Streams)
	}
	found := false
	for _, w := range tr.Run.Warnings {
		found = found || strings.HasPrefix(w, "skipped: locked: volume db")
	}
	if !found || !tr.Run.OK {
		t.Fatalf("run %+v", tr.Run)
	}
	// nothing of the locked volume was snapshotted
	if s := r.w.snapshots(t, SnapshotPrefix); len(s) != 0 {
		t.Fatalf("snapshots left: %v", s)
	}
}
