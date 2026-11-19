package service

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"jailops.net/jailops/internal/state"
	"jailops.net/jailops/internal/store"
	"jailops.net/jailops/sdk/api"
)

// JailOps targets FreeBSD 15.1 and later: the doctor fails for anything older and warns for a release newer than the
// newest tested one and for another major, because a new release is supported once the PF traffic matrix passes on it.
func TestDoctorChecksTheHostRelease(t *testing.T) {
	for release, want := range map[string]api.DoctorStatus{
		"14.3-RELEASE-p2": api.DoctorFail,
		"15.0-RELEASE-p1": api.DoctorFail,
		"15.1-RELEASE":    api.DoctorOK,
		"15.1-RELEASE-p3": api.DoctorOK,
		"15.2-STABLE":     api.DoctorWarn,
		"15.2-PRERELEASE": api.DoctorWarn,
		"16.0-CURRENT":    api.DoctorWarn,
		"nonsense":        api.DoctorUnknown,
	} {
		st, err := state.Open(t.TempDir(), state.Options{})
		if err != nil {
			t.Fatal(err)
		}
		db, err := store.Open(filepath.Join(t.TempDir(), "db"), store.Options{})
		if err != nil {
			t.Fatal(err)
		}
		rel := release
		s, err := New(Deps{State: st, DB: db, JournalDir: t.TempDir(), HostUUID: "h",
			Sysctl: func(context.Context, string) (string, error) { return rel, nil }})
		if err != nil {
			t.Fatal(err)
		}
		c := &checkSet{}
		s.doctorRelease(context.Background(), c)
		if len(c.list) != 1 || c.list[0].Status != want {
			t.Errorf("%s: %+v, want %s", release, c.list, want)
		}
		s.Close()
		db.Close()
		st.Close()
	}
	// unreadable
	st, _ := state.Open(t.TempDir(), state.Options{})
	db, _ := store.Open(filepath.Join(t.TempDir(), "db"), store.Options{})
	s, _ := New(Deps{State: st, DB: db, JournalDir: t.TempDir(), HostUUID: "h",
		Sysctl: func(context.Context, string) (string, error) { return "", errors.New("no sysctl") }})
	c := &checkSet{}
	s.doctorRelease(context.Background(), c)
	if len(c.list) != 1 || c.list[0].Status != api.DoctorUnknown {
		t.Errorf("%+v", c.list)
	}
	s.Close()
	db.Close()
	st.Close()
}

// The warning names the newest tested release and the page that holds the support matrix, and it never fails.
func TestDoctorReleaseWarningSaysWhatIsTested(t *testing.T) {
	for release, want := range map[string]string{
		"15.2-RELEASE": "newer than the newest release in the JailOps test matrix, 15.1",
		"16.0-RELEASE": "a major release JailOps does not cover yet (the newest in the test matrix is 15.1)",
	} {
		st, err := state.Open(t.TempDir(), state.Options{})
		if err != nil {
			t.Fatal(err)
		}
		db, err := store.Open(filepath.Join(t.TempDir(), "db"), store.Options{})
		if err != nil {
			t.Fatal(err)
		}
		rel := release
		s, err := New(Deps{State: st, DB: db, JournalDir: t.TempDir(), HostUUID: "h",
			Sysctl: func(context.Context, string) (string, error) { return rel, nil }})
		if err != nil {
			t.Fatal(err)
		}
		c := &checkSet{}
		s.doctorRelease(context.Background(), c)
		if len(c.list) != 1 || c.list[0].Status != api.DoctorWarn || !strings.Contains(c.list[0].Message, want) ||
			!strings.Contains(c.list[0].Hint, "docs/supported-versions.md") {
			t.Errorf("%s: %+v", release, c.list)
		}
		s.Close()
		db.Close()
		st.Close()
	}
}

// A host whose base system is installed from packages is information; one installed from distribution sets is a
// warning (FreeBSD 16 removes freebsd-update) and never a failure; where pkg cannot run the answer is unknown.
func TestDoctorPkgbase(t *testing.T) {
	missing := exec.Command("sh", "-c", "exit 1").Run() // an *exec.ExitError: pkg ran and said the package is not installed
	for name, tc := range map[string]struct {
		host func(ctx context.Context, name string, args ...string) ([]byte, error)
		want api.DoctorStatus
	}{
		"pkgbase": {func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name != "pkg" || strings.Join(args, " ") != "info -e FreeBSD-runtime" {
				t.Errorf("asked %s %v", name, args)
			}
			return nil, nil
		}, api.DoctorInfo},
		"distribution sets": {func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return nil, fmt.Errorf("pkg: %w", missing)
		}, api.DoctorWarn},
		"no pkg": {func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return nil, errors.New("pkg: not found")
		}, api.DoctorUnknown},
		"no way to ask": {nil, api.DoctorUnknown},
	} {
		st, err := state.Open(t.TempDir(), state.Options{})
		if err != nil {
			t.Fatal(err)
		}
		db, err := store.Open(filepath.Join(t.TempDir(), "db"), store.Options{})
		if err != nil {
			t.Fatal(err)
		}
		d := Deps{State: st, DB: db, JournalDir: t.TempDir(), HostUUID: "h"}
		if tc.host != nil {
			d.HostExec = tc.host
		}
		s, err := New(d)
		if err != nil {
			t.Fatal(err)
		}
		c := &checkSet{}
		s.doctorPkgbase(context.Background(), c)
		if len(c.list) != 1 || c.list[0].Name != "host.pkgbase" || c.list[0].Status != tc.want {
			t.Errorf("%s: %+v, want %s", name, c.list, tc.want)
		}
		if tc.want == api.DoctorWarn && (!strings.Contains(c.list[0].Hint, "freebsd-update") || strings.Contains(c.list[0].Hint, "fail")) {
			t.Errorf("%s: hint %q", name, c.list[0].Hint)
		}
		s.Close()
		db.Close()
		st.Close()
	}
}
