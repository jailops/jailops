package schedule

import (
	"crypto/sha256"
	"encoding/binary"
	"time"
)

// Jitter returns a deterministic delay in [0, window) derived from id (an
// object UUID or name). The same id always gets the same delay, so a fleet
// spreads its backups without any coordination and a restart does not move
// them. A window of zero or less returns zero.
func Jitter(id string, window time.Duration) time.Duration {
	if window <= 0 {
		return 0
	}
	h := sha256.Sum256([]byte("jailops/jitter/v1\x00" + id))
	n := binary.BigEndian.Uint64(h[:8])
	return time.Duration(n % uint64(window))
}

// NextWithJitter is Next plus the jitter for id. The jittered time is never
// before the plain fire time.
func (s *Spec) NextWithJitter(after time.Time, loc *time.Location, id string, window time.Duration) (time.Time, bool) {
	t, ok := s.Next(after, loc)
	if !ok {
		return t, false
	}
	return t.Add(Jitter(id, window)), true
}
