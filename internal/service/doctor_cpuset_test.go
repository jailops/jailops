package service

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"jailops.net/jailops/internal/state"
	"jailops.net/jailops/internal/store"
	"jailops.net/jailops/sdk/api"
)

func cpusetService(t *testing.T, ncpu string, sysErr error, jails ...api.Jail) *Service {
	t.Helper()
	st, err := state.Open(t.TempDir(), state.Options{})
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Deps{State: st, DB: db, JournalDir: t.TempDir(), HostUUID: "h",
		Sysctl: func(_ context.Context, name string) (string, error) {
			if name != "hw.ncpu" {
				t.Errorf("sysctl %s", name)
			}
			return ncpu, sysErr
		}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(); db.Close(); st.Close() })
	for _, j := range jails {
		s.jails[jailKey(j)] = &jailEntry{jail: j}
	}
	return s
}

func pinnedJail(name, set string) api.Jail {
	j := api.Jail{}
	j.Metadata.Name, j.Metadata.Project = name, "alpha"
	j.Spec.Resources.CPUSet = set
	return j
}

func TestDoctorChecksThePinnedCPUs(t *testing.T) {
	// no jail pinned: no line
	c := &checkSet{}
	cpusetService(t, "4", nil, pinnedJail("web", "")).doctorCPUSet(context.Background(), c)
	if len(c.list) != 0 {
		t.Fatalf("a host without pinned jails: %+v", c.list)
	}
	// every CPU exists
	c = &checkSet{}
	cpusetService(t, "4", nil, pinnedJail("web", "0-1"), pinnedJail("db", "3")).doctorCPUSet(context.Background(), c)
	if len(c.list) != 1 || c.list[0].Status != api.DoctorOK || !strings.Contains(c.list[0].Message, "2 jail(s)") {
		t.Fatalf("%+v", c.list)
	}
	// a smaller host than the one the profile was written for
	c = &checkSet{}
	cpusetService(t, "2", nil, pinnedJail("web", "0-1"), pinnedJail("db", "2,3")).doctorCPUSet(context.Background(), c)
	if len(c.list) != 1 || c.list[0].Status != api.DoctorWarn || !strings.Contains(c.list[0].Message, "alpha/db (2,3 names CPU 3)") ||
		strings.Contains(c.list[0].Message, "alpha/web") || !strings.Contains(c.list[0].Message, "2 CPUs (0-1)") {
		t.Fatalf("%+v", c.list)
	}
	// unreadable
	c = &checkSet{}
	cpusetService(t, "", errors.New("no sysctl"), pinnedJail("web", "0")).doctorCPUSet(context.Background(), c)
	if len(c.list) != 1 || c.list[0].Status != api.DoctorUnknown {
		t.Fatalf("%+v", c.list)
	}
}
