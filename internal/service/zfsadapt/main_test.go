package zfsadapt

import "time"

// the tests that make a dataset stay busy must not wait for the real retry period
func init() { unmountWait = time.Millisecond }
