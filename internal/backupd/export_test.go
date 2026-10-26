package backupd

// SetTestAdminUID lets the tests, which do not run as root, use the admin socket as an administrator. It is compiled
// only into the test binary, so the production package has no way to widen who is an administrator.
func SetTestAdminUID(uid *uint32) { testAdminUID = uid }
