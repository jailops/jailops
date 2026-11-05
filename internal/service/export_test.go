package service

// SetRenameCrash makes the next renames stop at step n as if the daemon died there (0 never).
func SetRenameCrash(s *Service, n int) { s.renameCrashAt = n }
