package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"jailops.net/jailops/sdk/api"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newStore(t *testing.T, opts Options) (*Store, *clock) {
	t.Helper()
	clk := &clock{t: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)}
	if opts.Now == nil {
		opts.Now = clk.Now
	}
	s, err := Open(filepath.Join(t.TempDir(), "db", "jailops.db"), opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, clk
}

var ctx = context.Background()

func h(s string) []byte { x := sha256.Sum256([]byte(s)); return x[:] }

func mkUser(t *testing.T, s *Store, id, name string) {
	t.Helper()
	if err := s.Users.Create(ctx, User{ID: id, Name: name, PasswordHash: "$argon2id$x", Permission: api.PermOperator}); err != nil {
		t.Fatal(err)
	}
}

func TestOpenMigratesAndReopens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.db")
	s, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	v, _ := s.Version(ctx)
	if v != SchemaVersion() || v < 1 {
		t.Fatalf("version %d", v)
	}
	var mode string
	if err := s.r.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal mode %q %v", mode, err)
	}
	s.Meta.Set(ctx, "k", "v")
	s.Close()
	s2, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if v, err := s2.Meta.Get(ctx, "k"); err != nil || v != "v" {
		t.Fatalf("%q %v", v, err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
}

func TestRefusesNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.db")
	s, _ := Open(path, Options{})
	s.Close()
	db, _ := sql.Open("sqlite", "file:"+path)
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", SchemaVersion()+1)); err != nil {
		t.Fatal(err)
	}
	db.Close()
	before, _ := os.ReadFile(path)
	_, err := Open(path, Options{})
	if !api.IsCode(err, api.CodeVersionSkew) {
		t.Fatalf("got %v", err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("database modified by refused open")
	}
}

func TestMigrationsWellFormed(t *testing.T) {
	ms, err := loadMigrations()
	if err != nil || len(ms) == 0 {
		t.Fatalf("%v", err)
	}
}

func TestUsers(t *testing.T) {
	s, clk := newStore(t, Options{})
	mkUser(t, s, "u1", "Alice")
	if err := s.Users.Create(ctx, User{ID: "u2", Name: "alice", PasswordHash: "x", Permission: api.PermAdmin}); !api.IsCode(err, api.CodeAlreadyExists) {
		t.Fatalf("case-insensitive uniqueness: %v", err)
	}
	if err := s.Users.Create(ctx, User{ID: "u2"}); !api.IsCode(err, api.CodeInvalid) {
		t.Fatalf("%v", err)
	}
	u, err := s.Users.GetByName(ctx, "ALICE")
	if err != nil || u.ID != "u1" || u.Disabled || u.CreatedAt.IsZero() {
		t.Fatalf("%v %+v", err, u)
	}
	if _, err := s.Users.GetByName(ctx, "nobody"); !api.IsCode(err, api.CodeNotFound) {
		t.Fatalf("%v", err)
	}
	clk.Add(time.Minute)
	if err := s.Users.SetTOTP(ctx, "u1", []byte{1, 2, 3}, true); err != nil {
		t.Fatal(err)
	}
	if err := s.Users.SetRecoveryHashes(ctx, "u1", []string{"a", "b", "c"}); err != nil {
		t.Fatal(err)
	}
	ok, err := s.Users.ConsumeRecoveryHash(ctx, "u1", "b")
	if !ok || err != nil {
		t.Fatalf("%v %v", ok, err)
	}
	if ok, _ := s.Users.ConsumeRecoveryHash(ctx, "u1", "b"); ok {
		t.Fatal("recovery code reusable")
	}
	u, _ = s.Users.GetByID(ctx, "u1")
	if !u.TOTPEnabled || !bytes.Equal(u.TOTPSealed, []byte{1, 2, 3}) || fmt.Sprint(u.RecoveryHashes) != "[a c]" || !u.UpdatedAt.After(u.CreatedAt) {
		t.Fatalf("%+v", u)
	}
	if err := s.Users.SetPermission(ctx, "u1", api.PermReadonly); err != nil {
		t.Fatal(err)
	}
	if err := s.Users.SetPermission(ctx, "zz", api.PermReadonly); !api.IsCode(err, api.CodeNotFound) {
		t.Fatalf("%v", err)
	}
	if err := s.Users.TouchLogin(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	mkUser(t, s, "u3", "bob")
	l, _ := s.Users.List(ctx)
	if len(l) != 2 || l[0].Name != "Alice" || l[1].Name != "bob" {
		t.Fatalf("%+v", l)
	}
	if err := s.Users.Delete(ctx, "u3"); err != nil {
		t.Fatal(err)
	}
	if err := s.Users.Delete(ctx, "u3"); !api.IsCode(err, api.CodeNotFound) {
		t.Fatalf("%v", err)
	}
}

func TestPasswordChangeAndDisableRevokeSessions(t *testing.T) {
	s, _ := newStore(t, Options{})
	mkUser(t, s, "u1", "a")
	for _, id := range []string{"s1", "s2"} {
		if _, err := s.Sessions.Create(ctx, h(id), "u1", "csrf", "1.2.3.4"); err != nil {
			t.Fatal(err)
		}
	}
	s.Users.SetPasswordHash(ctx, "u1", "new")
	if n, _ := s.Sessions.Count(ctx); n != 0 {
		t.Fatalf("%d sessions remain", n)
	}
	s.Sessions.Create(ctx, h("s3"), "u1", "csrf", "")
	s.Users.SetDisabled(ctx, "u1", true)
	if n, _ := s.Sessions.Count(ctx); n != 0 {
		t.Fatal("disable did not revoke")
	}
	if err := s.Users.SetPasswordHash(ctx, "u1", ""); !api.IsCode(err, api.CodeInvalid) {
		t.Fatal(err)
	}
}

func TestTokens(t *testing.T) {
	s, clk := newStore(t, Options{})
	exp := clk.Now().Add(time.Hour)
	tok := Token{ID: "t1", Name: "ci", Hash: h("jot_secret"), Permission: api.PermOperator, ExpiresAt: exp, AllowFrom: []string{"10.0.0.0/8"}}
	if err := s.Tokens.Create(ctx, tok); err != nil {
		t.Fatal(err)
	}
	if err := s.Tokens.Create(ctx, Token{ID: "t2", Name: "ci", Hash: h("other"), Permission: api.PermAdmin}); !api.IsCode(err, api.CodeAlreadyExists) {
		t.Fatalf("dup name: %v", err)
	}
	if err := s.Tokens.Create(ctx, Token{ID: "t3", Name: "x", Hash: h("jot_secret"), Permission: api.PermAdmin}); !api.IsCode(err, api.CodeAlreadyExists) {
		t.Fatalf("dup hash: %v", err)
	}
	if err := s.Tokens.Create(ctx, Token{ID: "t4", Name: "short", Hash: []byte{1}, Permission: api.PermAdmin}); !api.IsCode(err, api.CodeInvalid) {
		t.Fatalf("short hash: %v", err)
	}
	got, err := s.Tokens.GetByHash(ctx, h("jot_secret"))
	if err != nil || got.ID != "t1" || got.Permission != api.PermOperator || !got.ExpiresAt.Equal(exp) || fmt.Sprint(got.AllowFrom) != "[10.0.0.0/8]" {
		t.Fatalf("%v %+v", err, got)
	}
	if _, err := s.Tokens.GetByHash(ctx, h("nope")); !api.IsCode(err, api.CodeNotFound) {
		t.Fatalf("%v", err)
	}
	s.Tokens.Create(ctx, Token{ID: "t5", Name: "forever", Hash: h("f"), Permission: api.PermReadonly})
	s.Tokens.TouchUsed(ctx, "t1")
	got, _ = s.Tokens.Get(ctx, "t1")
	if got.LastUsedAt.IsZero() {
		t.Fatal("last used not set")
	}
	clk.Add(2 * time.Hour)
	if n, err := s.Tokens.DeleteExpired(ctx); n != 1 || err != nil {
		t.Fatalf("%d %v", n, err)
	}
	l, _ := s.Tokens.List(ctx)
	if len(l) != 1 || l[0].ID != "t5" || !l[0].ExpiresAt.IsZero() {
		t.Fatalf("%+v", l)
	}
	if err := s.Tokens.Delete(ctx, "t5"); err != nil {
		t.Fatal(err)
	}
	if err := s.Tokens.Delete(ctx, "t5"); !api.IsCode(err, api.CodeNotFound) {
		t.Fatalf("%v", err)
	}
	// the raw secret never appears in the database file
	raw, _ := os.ReadFile(s.path)
	if bytes.Contains(raw, []byte("jot_secret")) {
		t.Fatal("secret stored in clear")
	}
}

func TestSessionLease(t *testing.T) {
	s, clk := newStore(t, Options{})
	mkUser(t, s, "u1", "a")
	sess, err := s.Sessions.Create(ctx, h("sid"), "u1", "csrf1", "::1")
	if err != nil || !sess.IdleExpires.Equal(clk.Now().Add(30*time.Minute)) || !sess.MaxExpires.Equal(clk.Now().Add(12*time.Hour)) {
		t.Fatalf("%v %+v", err, sess)
	}
	// activity every 20 minutes keeps it alive
	for range 5 {
		clk.Add(20 * time.Minute)
		got, err := s.Sessions.Validate(ctx, h("sid"))
		if err != nil || got.CSRF != "csrf1" || got.UserID != "u1" {
			t.Fatalf("%v %+v", err, got)
		}
	}
	// idle expiry
	clk.Add(31 * time.Minute)
	if _, err := s.Sessions.Validate(ctx, h("sid")); !api.IsCode(err, api.CodeUnauthorized) {
		t.Fatalf("idle: %v", err)
	}
	if n, _ := s.Sessions.Count(ctx); n != 0 {
		t.Fatal("expired session not deleted")
	}
	// absolute expiry despite continuous activity; idle lease is clamped
	s.Sessions.Create(ctx, h("sid2"), "u1", "c", "")
	var last *Session
	for i := 0; i < 23; i++ {
		clk.Add(29 * time.Minute)
		last, err = s.Sessions.Validate(ctx, h("sid2"))
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		if last.IdleExpires.After(last.MaxExpires) {
			t.Fatal("idle beyond max")
		}
	}
	clk.Add(29 * time.Minute) // 24*29m = 11h36m... still before 12h
	clk.Add(30 * time.Minute) // now past 12h
	if _, err := s.Sessions.Validate(ctx, h("sid2")); !api.IsCode(err, api.CodeUnauthorized) {
		t.Fatalf("max lifetime: %v", err)
	}
	if _, err := s.Sessions.Validate(ctx, h("never")); !api.IsCode(err, api.CodeUnauthorized) {
		t.Fatalf("unknown: %v", err)
	}
}

func TestSessionPurgeLogoutAndDisabledUser(t *testing.T) {
	s, clk := newStore(t, Options{})
	mkUser(t, s, "u1", "a")
	s.Sessions.Create(ctx, h("a"), "u1", "c", "")
	s.Sessions.Create(ctx, h("b"), "u1", "c", "")
	clk.Add(time.Hour)
	s.Sessions.Create(ctx, h("c"), "u1", "c", "")
	if n, err := s.Sessions.PurgeExpired(ctx); n != 2 || err != nil {
		t.Fatalf("%d %v", n, err)
	}
	if err := s.Sessions.Delete(ctx, h("c")); err != nil {
		t.Fatal(err)
	}
	if err := s.Sessions.Delete(ctx, h("c")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Sessions.Create(ctx, h("d"), "ghost", "c", ""); err == nil {
		t.Fatal("foreign key not enforced")
	}
	if _, err := s.Sessions.Create(ctx, []byte{1}, "u1", "c", ""); !api.IsCode(err, api.CodeInvalid) {
		t.Fatal(err)
	}
	// user deletion cascades
	s.Sessions.Create(ctx, h("e"), "u1", "c", "")
	s.Users.Delete(ctx, "u1")
	if n, _ := s.Sessions.Count(ctx); n != 0 {
		t.Fatal("cascade failed")
	}
}

func TestOperationsPersistAndInterrupt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "o.db")
	clk := &clock{t: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)}
	s, _ := Open(path, Options{Now: clk.Now})
	mk := func(id string, st api.OperationStatus) api.Operation {
		return api.Operation{ID: id, Description: "d " + id, Status: st, MayCancel: true,
			Resources: map[string][]string{"jails": {"/api/v1/jails/web"}}, Metadata: json.RawMessage(`{"step":2}`),
			CreatedAt: clk.Now(), UpdatedAt: clk.Now()}
	}
	for i, st := range []api.OperationStatus{api.OpPending, api.OpRunning, api.OpSuccess, api.OpFailure, api.OpRunning} {
		clk.Add(time.Second)
		if err := s.Operations.Put(ctx, mk(fmt.Sprintf("op%d", i), st)); err != nil {
			t.Fatal(err)
		}
	}
	// update in place
	up := mk("op2", api.OpSuccess)
	up.Progress = 100
	s.Operations.Put(ctx, up)
	if err := s.Operations.Put(ctx, api.Operation{}); !api.IsCode(err, api.CodeInvalid) {
		t.Fatal(err)
	}
	s.Close() // simulated crash/restart
	s, err := Open(path, Options{Now: clk.Now})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.Operations.Get(ctx, "op2")
	if err != nil || got.Progress != 100 || string(got.Metadata) != `{"step":2}` || got.Resources["jails"][0] != "/api/v1/jails/web" {
		t.Fatalf("%v %+v", err, got)
	}
	if _, err := s.Operations.Get(ctx, "zz"); !api.IsCode(err, api.CodeNotFound) {
		t.Fatal(err)
	}
	clk.Add(time.Minute)
	ints, err := s.Operations.MarkInterrupted(ctx)
	if err != nil || len(ints) != 3 {
		t.Fatalf("%v %d", err, len(ints))
	}
	for _, id := range []string{"op0", "op1", "op4"} {
		op, _ := s.Operations.Get(ctx, id)
		if op.Status != api.OpFailure || op.Error == nil || op.MayCancel {
			t.Fatalf("%s: %+v", id, op)
		}
		if f, _ := s.Operations.WasInterrupted(ctx, id); !f {
			t.Fatalf("%s not flagged", id)
		}
	}
	if f, _ := s.Operations.WasInterrupted(ctx, "op3"); f {
		t.Fatal("finished op flagged")
	}
	if again, _ := s.Operations.MarkInterrupted(ctx); len(again) != 0 {
		t.Fatal("not idempotent")
	}
	l, _ := s.Operations.List(ctx, api.OpFailure, 0)
	if len(l) != 4 {
		t.Fatalf("%d failures", len(l))
	}
	all, _ := s.Operations.List(ctx, "", 2)
	if len(all) != 2 {
		t.Fatalf("limit: %d", len(all))
	}
	clk.Add(time.Hour)
	if n, _ := s.Operations.Prune(ctx, clk.Now().Add(-30*time.Minute)); n != 5 {
		t.Fatalf("pruned %d", n)
	}
}

func TestEventRing(t *testing.T) {
	s, clk := newStore(t, Options{EventMaxRows: 50, EventMaxAge: time.Hour})
	for i := range 130 {
		clk.Add(time.Second)
		_, err := s.Events.Append(ctx, api.Event{Type: api.EventLifecycle, Host: "h", Metadata: json.RawMessage(fmt.Sprintf(`{"i":%d}`, i))})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Events.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	n, _ := s.Events.Count(ctx)
	if n != 50 {
		t.Fatalf("ring holds %d", n)
	}
	evs, _ := s.Events.Since(ctx, 0, 0)
	if len(evs) != 50 || string(evs[49].Event.Metadata) != `{"i":129}` || evs[0].ID >= evs[1].ID {
		t.Fatalf("%d %+v", len(evs), evs[0])
	}
	tail, _ := s.Events.Since(ctx, evs[47].ID, 10)
	if len(tail) != 2 {
		t.Fatalf("since: %d", len(tail))
	}
	if evs[0].Event.Type != api.EventLifecycle || evs[0].Event.Timestamp.IsZero() {
		t.Fatalf("%+v", evs[0])
	}
	clk.Add(2 * time.Hour)
	if _, err := s.Events.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.Events.Count(ctx); n != 0 {
		t.Fatalf("age retention left %d", n)
	}
	// nil metadata round-trips as JSON null
	s.Events.Append(ctx, api.Event{Type: api.EventLogging})
	e, _ := s.Events.Since(ctx, 0, 1)
	if string(e[0].Event.Metadata) != "null" {
		t.Fatalf("%s", e[0].Event.Metadata)
	}
}

func TestAuditAppendOnly(t *testing.T) {
	s, clk := newStore(t, Options{})
	for i := range 5 {
		clk.Add(time.Second)
		actor := "alice"
		if i%2 == 1 {
			actor = "bob"
		}
		id, err := s.Audit.Append(ctx, AuditEntry{Actor: actor, ActorKind: "user", Method: "POST", Route: "/api/v1/jails", Object: fmt.Sprintf("jail%d", i), Result: "ok", Status: 202})
		if err != nil || id == 0 {
			t.Fatal(err)
		}
	}
	if _, err := s.Audit.Append(ctx, AuditEntry{Actor: "x"}); !api.IsCode(err, api.CodeInvalid) {
		t.Fatal(err)
	}
	all, _ := s.Audit.List(ctx, AuditFilter{})
	if len(all) != 5 || all[0].Object != "jail4" {
		t.Fatalf("%+v", all)
	}
	bob, _ := s.Audit.List(ctx, AuditFilter{Actor: "bob"})
	if len(bob) != 2 {
		t.Fatalf("%d", len(bob))
	}
	page, _ := s.Audit.List(ctx, AuditFilter{BeforeID: all[1].ID, Limit: 2})
	if len(page) != 2 || page[0].ID != all[2].ID {
		t.Fatalf("%+v", page)
	}
	since, _ := s.Audit.List(ctx, AuditFilter{Since: all[2].Time})
	if len(since) != 3 {
		t.Fatalf("since %d", len(since))
	}
	// Even raw SQL on the write connection cannot rewrite history.
	if _, err := s.w.Exec("UPDATE audit SET actor = 'mallory'"); err == nil {
		t.Fatal("update allowed")
	}
	if _, err := s.w.Exec("DELETE FROM audit"); err == nil {
		t.Fatal("delete allowed")
	}
}

func TestMeta(t *testing.T) {
	s, _ := newStore(t, Options{})
	if _, err := s.Meta.Get(ctx, "a"); !api.IsCode(err, api.CodeNotFound) {
		t.Fatal(err)
	}
	s.Meta.Set(ctx, "a", "1")
	s.Meta.Set(ctx, "a", "2")
	if v, _ := s.Meta.Get(ctx, "a"); v != "2" {
		t.Fatal(v)
	}
	s.Meta.Delete(ctx, "a")
	s.Meta.Delete(ctx, "a")
	if _, err := s.Meta.Get(ctx, "a"); !api.IsCode(err, api.CodeNotFound) {
		t.Fatal(err)
	}
	if err := s.Meta.Set(ctx, "", "x"); !api.IsCode(err, api.CodeInvalid) {
		t.Fatal(err)
	}
}

func TestVacuumIntoAndIntegrity(t *testing.T) {
	s, _ := newStore(t, Options{})
	mkUser(t, s, "u1", "alice")
	s.Tokens.Create(ctx, Token{ID: "t", Name: "n", Hash: h("x"), Permission: api.PermAdmin})
	if p, err := s.IntegrityCheck(ctx); err != nil || len(p) != 0 {
		t.Fatalf("%v %v", err, p)
	}
	dst := filepath.Join(t.TempDir(), "bk", "copy.db")
	if err := s.VacuumInto(ctx, dst); err != nil {
		t.Fatal(err)
	}
	if err := s.VacuumInto(ctx, dst); !api.IsCode(err, api.CodeAlreadyExists) {
		t.Fatalf("overwrite allowed: %v", err)
	}
	if st, _ := os.Stat(dst); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
	v, probs, err := OpenReadOnlyCheck(ctx, dst)
	if err != nil || v != SchemaVersion() || len(probs) != 0 {
		t.Fatalf("%d %v %v", v, probs, err)
	}
	// the copy opens as a normal store with the data
	c, err := Open(dst, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if u, err := c.Users.GetByName(ctx, "alice"); err != nil || u.ID != "u1" {
		t.Fatalf("%v", err)
	}
}

func TestIntegrityDetectsCorruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.db")
	s, _ := Open(path, Options{})
	for i := range 300 {
		s.Audit.Append(ctx, AuditEntry{Actor: "a", Method: "GET", Route: "/x", Result: "ok", Detail: fmt.Sprintf("%0200d", i)})
	}
	s.Close()
	raw, _ := os.ReadFile(path)
	for i := 4096 * 2; i < len(raw) && i < 4096*6; i++ {
		raw[i] ^= 0xff
	}
	os.WriteFile(path, raw, 0o600)
	_, probs, err := OpenReadOnlyCheck(ctx, path)
	if err == nil && len(probs) == 0 {
		t.Fatal("corruption not detected")
	}
}

func TestConcurrentUse(t *testing.T) {
	s, _ := newStore(t, Options{})
	mkUser(t, s, "u1", "a")
	var wg sync.WaitGroup
	for w := range 8 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for i := range 30 {
				if _, err := s.Audit.Append(ctx, AuditEntry{Actor: "a", Method: "POST", Route: "/r", Result: "ok", Object: fmt.Sprint(w, "-", i)}); err != nil {
					t.Error(err)
					return
				}
				if _, err := s.Events.Append(ctx, api.Event{Type: api.EventLifecycle}); err != nil {
					t.Error(err)
					return
				}
			}
		}()
		go func() {
			defer wg.Done()
			for range 60 {
				if _, err := s.Audit.List(ctx, AuditFilter{Limit: 10}); err != nil {
					t.Error(err)
					return
				}
				if _, err := s.Users.GetByName(ctx, "a"); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	l, _ := s.Audit.List(ctx, AuditFilter{Limit: 10000})
	if len(l) != 240 {
		t.Fatalf("%d audit rows", len(l))
	}
}

func TestReaderCannotWrite(t *testing.T) {
	s, _ := newStore(t, Options{})
	if _, err := s.r.Exec("INSERT INTO meta(key,value) VALUES('a','b')"); err == nil {
		t.Fatal("read pool accepted a write")
	}
}
