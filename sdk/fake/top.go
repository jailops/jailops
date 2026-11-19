package fake

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"jailops.net/jailops/sdk/api"
)

// SeedTop sets the frame GET /api/v1/top answers. Without it the fake answers a frame of the running jails of the
// request's project with fixed numbers and a host header.
func (s *Server) SeedTop(t api.Top) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.topFrame = &t
}

func (s *Server) top(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	rc := s.info(r)
	project := rc.project
	if v := r.URL.Query().Get("all_projects"); v == "true" || v == "1" {
		project = ""
	}
	var t api.Top
	if s.topFrame != nil {
		t = *s.topFrame
		t.Jails = append([]api.TopJail(nil), t.Jails...)
	} else {
		t = api.Top{Jails: []api.TopJail{}}
		cpu, mem, procs, disk, in, out := 12.5, int64(407048192), 23, int64(268435456), int64(2048), int64(1024)
		for _, j := range s.jails.items {
			if j.Status.Power != api.PowerRunning || (project != "" && j.Metadata.Project != project) || !rc.canProject(j.Metadata.Project) {
				continue
			}
			t.Jails = append(t.Jails, api.TopJail{Project: j.Metadata.Project, Name: j.Metadata.Name, UptimeSeconds: 3600,
				CPUPercent: &cpu, MemoryBytes: &mem, Processes: &procs, DiskBytes: &disk, NetInBytesPerSecond: &in, NetOutBytesPerSecond: &out})
		}
		sortTop(t.Jails)
		busy, used, total := 7.5, uint64(1<<30), uint64(4<<30)
		t.Host = &api.TopHost{Load1: 0.5, Load5: 0.4, Load15: 0.3, CPUs: 4, CPUPercent: &busy, MemoryTotal: total, MemoryUsed: &used, RunningJails: len(t.Jails)}
	}
	if t.MeasuredAt.IsZero() {
		t.MeasuredAt = s.now().UTC().Truncate(time.Second)
	}
	// the labels narrow the jails as they do for the list
	if want := r.URL.Query()["label"]; len(want) > 0 {
		keep := []api.TopJail{}
		for _, tj := range t.Jails {
			j := s.jails.items[s.jails.keyIn(tj.Project, tj.Name)]
			if j == nil {
				continue
			}
			ok := true
			for _, l := range want {
				k, v, _ := strings.Cut(l, "=")
				if j.Metadata.Labels[k] != v {
					ok = false
				}
			}
			if ok {
				keep = append(keep, tj)
			}
		}
		t.Jails = keep
	}
	s.mu.Unlock()
	writeSync(w, 200, t)
}

func sortTop(j []api.TopJail) {
	sort.Slice(j, func(a, b int) bool {
		if j[a].Project != j[b].Project {
			return j[a].Project < j[b].Project
		}
		return j[a].Name < j[b].Name
	})
}
