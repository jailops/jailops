package service

import (
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
)

// The upgrade of a jail holds the jail's own lock for the whole operation (Submit takes the locks of the OpSpec and keeps
// them until the operation ends): a second operation on the same jail (a second upgrade, update apply, shell setup,
// provision, an app install) is refused with busy and the holder named; another jail is another key, and there is no
// global lock.
func TestUpgradeLocksOneJailAndNoOther(t *testing.T) {
	l := newLockSet()
	first, err := l.tryLock("Upgrade jail web", jailLock("demo_web"), anchorLock("demo_web"))
	if err != nil {
		t.Fatal(err)
	}
	// the same jail: busy, naming what holds it
	_, err = l.tryLock("Upgrade jail web", jailLock("demo_web"), anchorLock("demo_web"))
	if !api.IsCode(err, api.CodeBusy) || !strings.Contains(err.Error(), "Upgrade jail web") {
		t.Fatalf("second upgrade of the same jail: %v", err)
	}
	// another jail: both run
	second, err := l.tryLock("Upgrade jail db", jailLock("demo_db"), anchorLock("demo_db"))
	if err != nil {
		t.Fatalf("a different jail was held: %v", err)
	}
	second()
	first()
	if third, err := l.tryLock("Upgrade jail web", jailLock("demo_web")); err != nil {
		t.Fatal(err)
	} else {
		third()
	}
}
