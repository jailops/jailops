package daemon

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"jailops.net/jailops/internal/store"
)

func TestHostIdentityIsCreatedOnceAndKept(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "jailops.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a, err := hostIdentity(ctx, db)
	if err != nil || a == "" {
		t.Fatalf("first start: %q %v", a, err)
	}
	b, err := hostIdentity(ctx, db)
	if err != nil || b != a {
		t.Fatalf("second start must keep %q, got %q %v", a, b, err)
	}
}

func TestHostIdentityReadErrorStopsTheStart(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "jailops.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hostIdentity(ctx, db); err != nil {
		t.Fatal(err)
	}
	// a closed database is a read error, not a missing key
	db.Close()
	if id, err := hostIdentity(ctx, db); err == nil || id != "" {
		t.Fatalf("a read error must stop the start, got %q %v", id, err)
	}
}

func TestHostIdentityEmptyValueStopsTheStart(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "jailops.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Meta.Set(ctx, "host_uuid", ""); err != nil {
		t.Fatal(err)
	}
	_, err = hostIdentity(ctx, db)
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("an empty stored uuid must stop the start: %v", err)
	}
}
