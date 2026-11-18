package service

import (
	"slices"
	"testing"

	"jailops.net/jailops/internal/render"
	"jailops.net/jailops/sdk/api"
)

func TestFstabTargetsAreTheMountPoints(t *testing.T) {
	fs, err := render.Fstab(render.FstabInput{OS: api.OSLinux, Root: "/j/root", Volumes: []render.FstabVolume{{Source: "/vol/a", Target: "/srv/a"}}, DevfsRuleset: 0})
	if err != nil {
		t.Fatal(err)
	}
	got := fstabTargets(fs)
	for _, want := range []string{"/j/root/dev", "/j/root/compat/linux", "/j/root/compat/linux/proc", "/j/root/compat/linux/sys", "/j/root/srv/a", "/j/root/compat/linux/srv/a"} {
		if !slices.Contains(got, want) {
			t.Errorf("%q is not among %q", want, got)
		}
	}
}
