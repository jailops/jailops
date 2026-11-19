package cli_test

import "testing"

// TestVolumeCreateFromDir covers copying a host directory into a new volume: the note that says what came across, the
// refusals (a directory that is not allowed, a path and --from-dir together) and --dry-run.
func TestVolumeCreateFromDir(t *testing.T) {
	h := newHarness(t)
	h.seed()
	tr := h.transcript()
	tr.ok("--dry-run", "volume", "create", "site", "--from-dir", "/srv/data/site")
	tr.do("", "volume", "create", "site", "--from-dir", "/srv/data/site", "--path", "/mnt/x")
	tr.do("", "volume", "create", "etc", "--from-dir", "/etc")
	tr.ok("volume", "create", "site", "--from-dir", "/srv/data/site", "--uid", "80", "--gid", "80")
	tr.golden("volume_from_dir")
}
