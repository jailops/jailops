package imagegen

import (
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
)

// The ABI of an image is the one its release names, whatever ABI the host's pkg reports: a host of one major release
// builds the images of another.
func TestImageABIIsTheReleasesNotTheHosts(t *testing.T) {
	w := newWorld(t)
	w.hostABI = "FreeBSD:16:amd64"
	w.repoVersion = "15.0p1"
	if _, err := w.M.Build(bg, "fb", api.ImageBuild{}); err != nil {
		t.Fatal(err)
	}
	var sawPkg bool
	for _, c := range w.Exec.Calls {
		line := strings.Join(c, " ")
		if len(c) > 1 && c[0] == "pkg" && c[1] == "-r" {
			sawPkg = true
			if !strings.Contains(line, "ABI=FreeBSD:15:amd64") || strings.Contains(line, "FreeBSD:16") {
				t.Fatalf("pkg ran with another ABI than the release's: %s", line)
			}
		}
	}
	if !sawPkg {
		t.Fatal("no pkg command ran")
	}
}

func TestBuildRefusesPackagesOfAnotherRelease(t *testing.T) {
	w := newWorld(t)
	w.repoVersion = "15.1p3" // the image is for 15.0-RELEASE
	_, err := w.M.Build(bg, "fb", api.ImageBuild{})
	if err == nil || !strings.Contains(err.Error(), "verify-release") || !strings.Contains(err.Error(), "15.1p3") {
		t.Fatalf("a build with the packages of another release: %v", err)
	}
	if o, err := w.M.Observe(bg, "fb"); err != nil || o.MaxNumber != 0 {
		t.Fatalf("a refused build left a generation: %+v, %v", o, err)
	}
}

func TestVerifyBase(t *testing.T) {
	for _, tc := range []struct {
		version string
		ok      bool
	}{
		{"15.1", true}, {"15.1p7", true}, {"15.1_2", true}, {"15.10", false}, {"15.2p1", false}, {"14.1", false}, {"", false}, {"v1", false},
	} {
		if err := VerifyBase(tc.version, 15, 1, "15.1-RELEASE"); (err == nil) != tc.ok {
			t.Errorf("VerifyBase(%q) = %v, want ok=%v", tc.version, err, tc.ok)
		}
	}
}
