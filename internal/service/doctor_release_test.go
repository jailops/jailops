package service

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"jailops.net/jailops/internal/state"
	"jailops.net/jailops/internal/store"
	"jailops.net/jailops/sdk/api"
)

// JailOps targets FreeBSD 15.1 and later; the doctor says so for anything older and stays quiet about newer releases.
func TestDoctorChecksTheHostRelease(t *testing.T) {
	for release, want := range map[string]api.DoctorStatus{
		"14.3-RELEASE-p2": api.DoctorFail,
		"15.0-RELEASE-p1": api.DoctorFail,
		"15.1-RELEASE":    api.DoctorOK,
		"15.1-RELEASE-p3": api.DoctorOK,
		"15.2-STABLE":     api.DoctorOK,
		"16.0-CURRENT":    api.DoctorOK,
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
