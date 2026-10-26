package store

import (
	"database/sql"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jailops.net/jailops/sdk/api"
)

func TestMigrationV1IdentitiesBecomeGlobal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	ms, _ := loadMigrations()
	if _, err := db.Exec(ms[0].sql); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version = 1"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users (id,name,password_hash,permission,created_at,updated_at) VALUES ('u1','old','x','admin',1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO tokens (id,name,hash,permission,created_at) VALUES ('t1','oldtok',?, 'admin',1)`, h("a")); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	u, err := s.Users.GetByName(ctx, "old")
	if err != nil || !u.Global {
		t.Fatalf("%+v %v", u, err)
	}
	tk, err := s.Tokens.Get(ctx, "t1")
	if err != nil || !tk.Global {
		t.Fatalf("%+v %v", tk, err)
	}
	// New rows default to neither.
	mkUser(t, s, "u2", "new")
	u2, _ := s.Users.GetByName(ctx, "new")
	if u2.Global || len(u2.Projects) != 0 {
		t.Fatalf("%+v", u2)
	}
}

func TestScopePersists(t *testing.T) {
	s, _ := newStore(t, Options{})
	if err := s.Users.Create(ctx, User{ID: "u", Name: "ann", PasswordHash: "x", Permission: api.PermOperator, Projects: []string{"web"}}); err != nil {
		t.Fatal(err)
	}
	u, _ := s.Users.GetByID(ctx, "u")
	if len(u.Projects) != 1 || u.Projects[0] != "web" || u.Global {
		t.Fatalf("%+v", u)
	}
	if err := s.Users.SetScope(ctx, "u", []string{"a", "b"}, true); err != nil {
		t.Fatal(err)
	}
	u, _ = s.Users.GetByID(ctx, "u")
	if len(u.Projects) != 2 || !u.Global {
		t.Fatalf("%+v", u)
	}
	if err := s.Tokens.Create(ctx, Token{ID: "t", Name: "tok", Hash: h("x"), Permission: api.PermReadonly, Projects: []string{"web"}}); err != nil {
		t.Fatal(err)
	}
	l, _ := s.Tokens.List(ctx)
	if len(l) != 1 || l[0].Projects[0] != "web" || l[0].Global {
		t.Fatalf("%+v", l)
	}
}

func TestTrustTokens(t *testing.T) {
	s, clk := newStore(t, Options{})
	now := clk.Now()
	tt := TrustToken{ID: "a1", Name: "laptop", Permission: api.PermOperator, Projects: []string{"web"}, ExpiresAt: now.Add(time.Hour), CreatedBy: "admin"}
	if err := s.TrustTokens.Create(ctx, tt); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TrustTokens.Consume(ctx, "nope", "x", now); err != ErrTrustTokenUnusable {
		t.Fatalf("%v", err)
	}
	got, err := s.TrustTokens.Consume(ctx, "a1", "fp1", now)
	if err != nil || got.Name != "laptop" || got.ConsumedBy != "fp1" || got.Projects[0] != "web" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := s.TrustTokens.Consume(ctx, "a1", "fp2", now); !api.IsCode(err, api.CodeUnauthorized) {
		t.Fatalf("replay: %v", err)
	}
	// Expired.
	s.TrustTokens.Create(ctx, TrustToken{ID: "a2", Name: "x", Permission: api.PermReadonly, Global: true, ExpiresAt: now.Add(time.Minute)})
	if _, err := s.TrustTokens.Consume(ctx, "a2", "fp", now.Add(time.Minute)); err != ErrTrustTokenUnusable {
		t.Fatalf("expired: %v", err)
	}
	// Concurrent consumption succeeds once.
	s.TrustTokens.Create(ctx, TrustToken{ID: "a3", Name: "y", Permission: api.PermReadonly, Global: true, ExpiresAt: now.Add(time.Hour)})
	var ok atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.TrustTokens.Consume(ctx, "a3", "c", now); err == nil {
				ok.Add(1)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 1 {
		t.Fatalf("%d consumers won", ok.Load())
	}
	if l, _ := s.TrustTokens.List(ctx); len(l) != 3 {
		t.Fatalf("%d", len(l))
	}
	if n, _ := s.TrustTokens.PruneExpired(ctx, now.Add(2*time.Hour)); n != 3 {
		t.Fatalf("pruned %d", n)
	}
}

func TestSessionListAndRevokeByRef(t *testing.T) {
	s, _ := newStore(t, Options{})
	mkUser(t, s, "u1", "ann")
	s.Sessions.Create(ctx, h("s1"), "u1", "c", "10.0.0.1")
	s.Sessions.Create(ctx, h("s2"), "u1", "c", "10.0.0.2")
	l, err := s.Sessions.List(ctx)
	if err != nil || len(l) != 2 || l[0].UserName != "ann" || len(l[0].Ref) != 16 {
		t.Fatalf("%+v %v", l, err)
	}
	if ok, _ := s.Sessions.DeleteByRef(ctx, "zz"); ok {
		t.Fatal("bad ref")
	}
	if ok, _ := s.Sessions.DeleteByRef(ctx, SessionRef(h("s1"))); !ok {
		t.Fatal("not revoked")
	}
	if _, err := s.Sessions.Validate(ctx, h("s1")); err == nil {
		t.Fatal("revoked session still valid")
	}
	if l, _ := s.Sessions.List(ctx); len(l) != 1 {
		t.Fatalf("%d", len(l))
	}
}

func TestAuditProjectFilter(t *testing.T) {
	s, _ := newStore(t, Options{})
	for _, p := range []string{"", "web", "db", "web"} {
		if _, err := s.Audit.Append(ctx, AuditEntry{Actor: "a", Method: "POST", Route: "/r", Result: "ok", Project: p}); err != nil {
			t.Fatal(err)
		}
	}
	l, _ := s.Audit.List(ctx, AuditFilter{ProjectFilter: true, Projects: []string{"web"}})
	if len(l) != 2 || l[0].Project != "web" {
		t.Fatalf("%+v", l)
	}
	if l, _ := s.Audit.List(ctx, AuditFilter{ProjectFilter: true}); len(l) != 0 {
		t.Fatalf("empty list returned %d", len(l))
	}
	if l, _ := s.Audit.List(ctx, AuditFilter{}); len(l) != 4 {
		t.Fatalf("%d", len(l))
	}
}

func TestOperationsListForProjects(t *testing.T) {
	s, clk := newStore(t, Options{})
	for i, p := range []string{"web", "db", ""} {
		now := clk.Now().Add(time.Duration(i) * time.Second)
		if err := s.Operations.Put(ctx, api.Operation{ID: string(rune('a' + i)), Status: api.OpSuccess, Project: p, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	if l, _ := s.Operations.ListForProjects(ctx, true, nil, 0); len(l) != 3 || l[0].ID != "c" {
		t.Fatalf("%d", len(l))
	}
	if l, _ := s.Operations.ListForProjects(ctx, false, []string{"web", "db"}, 0); len(l) != 2 || l[0].ID != "b" {
		t.Fatalf("%+v", l)
	}
	if l, _ := s.Operations.ListForProjects(ctx, false, nil, 0); len(l) != 0 {
		t.Fatal("empty list matched")
	}
}
