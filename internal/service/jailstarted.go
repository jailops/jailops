package service

import (
	"context"
	"strconv"
	"strings"
	"time"
)

// recoverStartTimes gives the jails that already run when the daemon starts their start time (status.started_at,
// the UPTIME and STARTED of `jailops list -v`), which is otherwise known only for a start the daemon made itself.
// The kernel keeps no creation time of a jail; the oldest process in it is the nearest: what the start ran (rc and
// the services it started) is there from the start on. `ps -ax -J JID -o etimes=` gives the elapsed seconds of each
// process; the largest is taken. A jail without processes, or a host where ps cannot be read, stays without one.
func (s *Service) recoverStartTimes(ctx context.Context) {
	if s.d.HostExec == nil || s.d.Jails == nil {
		return
	}
	running := s.runningJails(ctx)
	type want struct {
		key string
		jid int
	}
	var list []want
	s.mu.RLock()
	for key, jid := range running {
		if _, ok := s.jails[key]; ok && s.status[key].startedAt.IsZero() && jid > 0 {
			list = append(list, want{key, jid})
		}
	}
	s.mu.RUnlock()
	for _, w := range list {
		cctx, cancel := context.WithTimeout(ctx, topCallTimeout)
		out, err := s.d.HostExec(cctx, "ps", "-ax", "-J", strconv.Itoa(w.jid), "-o", "etimes=")
		cancel()
		if err != nil {
			continue
		}
		up, ok := oldestElapsed(string(out))
		if !ok {
			continue
		}
		at := s.now().UTC().Add(-up).Truncate(time.Second)
		s.mu.Lock()
		if rt := s.status[w.key]; rt.startedAt.IsZero() {
			rt.startedAt = at
			s.status[w.key] = rt
		}
		s.mu.Unlock()
	}
}

// oldestElapsed is the largest number of seconds in the output of `ps -o etimes=` (one per line).
func oldestElapsed(out string) (time.Duration, bool) {
	best, ok := int64(-1), false
	for _, l := range strings.Split(out, "\n") {
		n, err := strconv.ParseInt(strings.TrimSpace(l), 10, 64)
		if err != nil || n < 0 {
			continue
		}
		if n > best {
			best, ok = n, true
		}
	}
	return time.Duration(best) * time.Second, ok
}
