package auth

import (
	"strings"
	"sync"
	"testing"
	"time"

	"jailops.net/jailops/sdk/api"
)

var joinKey = []byte("0123456789abcdef0123456789abcdef")

const fp = "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"

func TestJoinTokenLifecycle(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	tok, c, err := NewJoinToken(joinKey, "node2", strings.ToUpper(fp), []string{"10.0.0.1:8443"}, time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok, "joj_") || c.ClusterFingerprint != fp || c.Member != "node2" || !c.Expires.Equal(now.Add(time.Hour)) {
		t.Fatalf("%s %+v", tok, c)
	}
	used := &MemoryUsedSet{Now: func() time.Time { return now }}
	got, err := VerifyJoinToken(joinKey, tok, "node2", now.Add(time.Minute), used)
	if err != nil || got.ID != c.ID || got.ClusterFingerprint != fp || got.Addresses[0] != "10.0.0.1:8443" {
		t.Fatalf("%v %+v", err, got)
	}
	if _, err := VerifyJoinToken(joinKey, tok, "node2", now.Add(time.Minute), used); !api.IsCode(err, api.CodeUnauthorized) {
		t.Fatalf("reuse: %v", err)
	}
}

func TestJoinTokenRejections(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	mk := func() string { tok, _, _ := NewJoinToken(joinKey, "node2", fp, nil, time.Hour, now); return tok }
	flip := func(s string) string {
		b := []byte(s)
		i := len(b) - 3
		if b[i] == 'A' {
			b[i] = 'B'
		} else {
			b[i] = 'A'
		}
		return string(b)
	}
	tok := mk()
	payload, sig, _ := strings.Cut(strings.TrimPrefix(tok, "joj_"), ".")
	otherTok, _, _ := NewJoinToken(joinKey, "node2", fp, nil, time.Hour, now)
	otherPayload, _, _ := strings.Cut(strings.TrimPrefix(otherTok, "joj_"), ".")
	tests := []struct {
		name   string
		token  string
		key    []byte
		member string
		at     time.Time
		code   api.Code
	}{
		{"expired", tok, joinKey, "node2", now.Add(time.Hour), api.CodeUnauthorized},
		{"wrong member", tok, joinKey, "node3", now, api.CodeForbidden},
		{"wrong key", tok, []byte("ffffffffffffffffffffffffffffffff"), "node2", now, api.CodeUnauthorized},
		{"tampered mac", flip(tok), joinKey, "node2", now, api.CodeUnauthorized},
		{"payload swapped", "joj_" + otherPayload + "." + sig, joinKey, "node2", now, api.CodeUnauthorized},
		{"no prefix", strings.TrimPrefix(tok, "joj_"), joinKey, "node2", now, api.CodeUnauthorized},
		{"no dot", "joj_" + payload, joinKey, "node2", now, api.CodeUnauthorized},
		{"empty", "", joinKey, "node2", now, api.CodeUnauthorized},
		{"garbage", "joj_!!.!!", joinKey, "node2", now, api.CodeUnauthorized},
	}
	for _, tc := range tests {
		_, err := VerifyJoinToken(tc.key, tc.token, tc.member, tc.at, &MemoryUsedSet{})
		if !api.IsCode(err, tc.code) {
			t.Errorf("%s: %v want %s", tc.name, err, tc.code)
		}
	}
	// a rejected attempt must not burn the token
	used := &MemoryUsedSet{}
	if _, err := VerifyJoinToken(joinKey, tok, "wrong", now, used); err == nil {
		t.Fatal("wrong member accepted")
	}
	if _, err := VerifyJoinToken(joinKey, tok, "node2", now, used); err != nil {
		t.Fatalf("token burned by a rejected attempt: %v", err)
	}
}

func TestNewJoinTokenValidation(t *testing.T) {
	now := time.Now()
	for name, f := range map[string]func() error{
		"short key": func() error { _, _, e := NewJoinToken([]byte("k"), "n", fp, nil, time.Hour, now); return e },
		"no member": func() error { _, _, e := NewJoinToken(joinKey, "", fp, nil, time.Hour, now); return e },
		"slash":     func() error { _, _, e := NewJoinToken(joinKey, "a/b", fp, nil, time.Hour, now); return e },
		"bad fp":    func() error { _, _, e := NewJoinToken(joinKey, "n", "xyz", nil, time.Hour, now); return e },
		"zero ttl":  func() error { _, _, e := NewJoinToken(joinKey, "n", fp, nil, 0, now); return e },
		"negative":  func() error { _, _, e := NewJoinToken(joinKey, "n", fp, nil, -time.Second, now); return e },
	} {
		if f() == nil {
			t.Errorf("%s accepted", name)
		}
	}
	a, _, _ := NewJoinToken(joinKey, "n", fp, nil, time.Hour, now)
	b, _, _ := NewJoinToken(joinKey, "n", fp, nil, time.Hour, now)
	if a == b {
		t.Fatal("tokens not unique")
	}
}

func TestJoinSingleUseUnderConcurrency(t *testing.T) {
	now := time.Now()
	tok, _, _ := NewJoinToken(joinKey, "n", fp, nil, time.Hour, now)
	used := &MemoryUsedSet{}
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := VerifyJoinToken(joinKey, tok, "n", now, used); err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d winners", wins)
	}
}

func TestMemoryUsedSetForgetsExpired(t *testing.T) {
	now := time.Now()
	s := &MemoryUsedSet{Now: func() time.Time { return now }}
	if ok, _ := s.Consume("a", now.Add(time.Minute)); !ok {
		t.Fatal()
	}
	now = now.Add(time.Hour)
	s.Consume("b", now.Add(time.Minute))
	s.mu.Lock()
	n := len(s.m)
	s.mu.Unlock()
	if n != 1 {
		t.Fatalf("%d entries", n)
	}
}
