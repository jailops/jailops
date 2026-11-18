package service

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"jailops.net/jailops/sdk/api"
)

// lockSet holds per-resource locks. Keys look like "jail/web", "network/lan",
// "image/fb" and "anchor/web". Locks are not reentrant: an operation takes all
// the keys it needs at once and gets api.CodeBusy if any is held.
type lockSet struct {
	mu   sync.Mutex
	held map[string]string // key -> holder description
}

func newLockSet() *lockSet { return &lockSet{held: map[string]string{}} }

// tryLock takes every key or none. The returned function releases them.
func (l *lockSet) tryLock(holder string, keys ...string) (func(), error) {
	keys = slices.Clone(keys)
	slices.Sort(keys)
	keys = slices.Compact(keys)
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range keys {
		if h, ok := l.held[k]; ok {
			return nil, api.Errorf(api.CodeBusy, "%s is busy: %s is running", k, h).
				WithHint("wait for the other operation to finish").WithDetail("resource", k)
		}
	}
	for _, k := range keys {
		l.held[k] = holder
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			for _, k := range keys {
				delete(l.held, k)
			}
			l.mu.Unlock()
		})
	}, nil
}

// reconcileWait is how long an operation a person asked for waits for the background reconcile of a resource.
const reconcileWait = 20 * time.Second

// lockPatient is tryLock for an operation a caller asked for, and for the start of a jail at boot: when the only thing in
// the way is the daemon's own short work (a reconcile pass, the preparation of the same network by another jail's
// start, the housekeeping pass or the clean-up of the mounts of the jail, over in moments), it waits for it instead of
// answering "busy": several jails of one network can be started at once, and a daemon that has just started does not
// refuse the first operation or leave a jail of the boot to the next pass.
func (l *lockSet) lockPatient(ctx context.Context, holder string, keys ...string) (func(), error) {
	deadline := time.Now().Add(reconcileWait)
	for {
		release, err := l.tryLock(holder, keys...)
		if err == nil || !api.IsCode(err, api.CodeBusy) || time.Now().After(deadline) || !l.onlyReconcile(keys) {
			return release, err
		}
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// daemonWork are the holders of the daemon's own short work: what an operation waits for.
var daemonWork = []string{"reconcile", "prepare network ", "housekeeping ", "heal jail "}

// onlyReconcile reports whether every held key among keys is held by the daemon's own short work.
func (l *lockSet) onlyReconcile(keys []string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range keys {
		if h, ok := l.held[k]; ok && !slices.ContainsFunc(daemonWork, func(p string) bool { return strings.HasPrefix(h, p) }) {
			return false
		}
	}
	return true
}

// isHeld reports whether a key is locked (used by reconcile to skip).
func (l *lockSet) isHeld(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.held[key]
	return ok
}

func jailLock(n string) string    { return "jail/" + n }
func networkLock(n string) string { return "network/" + n }
func imageLock(n string) string   { return "image/" + n }
func anchorLock(n string) string  { return "anchor/" + n }

// volumeLock is the lock of a volume of a project.
func volumeLock(project, name string) string { return "volume/" + project + "/" + name }
