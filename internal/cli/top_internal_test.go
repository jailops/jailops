package cli

import (
	"context"
	"time"
)

// SetTopSleep replaces the wait between two frames of `jailops top` and returns the restore.
func SetTopSleep(f func(ctx context.Context, d time.Duration) error) func() {
	old := topSleep
	topSleep = f
	return func() { topSleep = old }
}
