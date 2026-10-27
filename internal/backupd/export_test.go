package backupd

import "time"

// SetTestAdminUID lets the tests, which do not run as root, use the admin socket as an administrator. It is compiled
// only into the test binary, so the production package has no way to widen who is an administrator.
func SetTestAdminUID(uid *uint32) { testAdminUID = uid }

// SetHashSlots sets how many password hashes the management API runs at once (tests).
func (s *Server) SetHashSlots(n int) { s.mgmt.hashSem = make(chan struct{}, n) }

// SetReadDeadline shortens how long a management request may take to arrive; it returns what puts the old value back (tests).
func SetReadDeadline(d time.Duration) func() {
	old := readDeadline
	readDeadline = d
	return func() { readDeadline = old }
}
