package format

import (
	"testing"
	"time"
)

// With a frozen clock the 12 random bits of an id repeat in one case of 4096: two ids made one after the other never are the same.
func TestSnapshotIDsOfAFrozenClockDifferInARow(t *testing.T) {
	at := time.Date(2026, 11, 24, 7, 0, 0, 123456000, time.UTC)
	prev := ""
	for i := 0; i < 100000; i++ {
		id, err := NewSnapshotID(at, nil)
		if err != nil {
			t.Fatal(err)
		}
		if id == prev {
			t.Fatalf("id %s was made twice in a row (after %d)", id, i)
		}
		prev = id
	}
}
