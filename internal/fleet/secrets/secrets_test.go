package secrets

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newStore(t *testing.T) (*Store, string, *Redactor) {
	t.Helper()
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	idf := filepath.Join(dir, "id.txt")
	if err := os.WriteFile(idf, []byte(id.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, RecipientsFile), []byte("# team\n"+id.Recipient().String()+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	red := NewRedactor()
	return NewStore(dir, Source{File: idf, Getenv: func(string) string { return "" }}, red), idf, red
}

func TestRoundTripAndRedaction(t *testing.T) {
	st, _, red := newStore(t)
	rs, err := st.Recipients()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Set("db", []byte("hunter2-hunter2\n"), rs); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(st.dir, "db.age"))
	if bytes.Contains(raw, []byte("hunter2")) {
		t.Fatal("ciphertext contains the plaintext")
	}
	tok, err := st.Token("db")
	if err != nil || tok != "hunter2-hunter2" {
		t.Fatalf("%q %v", tok, err)
	}
	if got := red.String("password=hunter2-hunter2;"); got != "password="+Mask+";" {
		t.Fatalf("not redacted: %q", got)
	}
	var buf bytes.Buffer
	_, _ = red.Writer(&buf).Write([]byte("line hunter2-hunter2\n"))
	if strings.Contains(buf.String(), "hunter2") {
		t.Fatal("writer leaked")
	}
	names, _ := st.Names()
	if len(names) != 1 || names[0] != "db" {
		t.Fatalf("%v", names)
	}
}

func TestSealedNeedsNoIdentity(t *testing.T) {
	st, _, _ := newStore(t)
	rs, _ := st.Recipients()
	if err := st.Set("k", []byte("value-value"), rs); err != nil {
		t.Fatal(err)
	}
	anon := NewStore(st.dir, Source{Getenv: func(string) string { return "" }}, nil)
	s, err := anon.Sealed("k")
	if err != nil || !strings.HasPrefix(s, SealedPrefix) {
		t.Fatalf("%q %v", s, err)
	}
	if _, err := anon.Decrypt("k"); err == nil || !strings.Contains(err.Error(), "no age identity") {
		t.Fatalf("got %v", err)
	}
	if err := os.WriteFile(filepath.Join(st.dir, "plain.age"), []byte("not encrypted"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := anon.Sealed("plain"); err == nil {
		t.Fatal("a plaintext file was accepted as sealed")
	}
}

func TestIdentitySources(t *testing.T) {
	_, idf, _ := newStore(t)
	b, _ := os.ReadFile(idf)
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if _, err := (Source{Getenv: env(nil)}).Load(); err != ErrNoIdentity {
		t.Fatalf("got %v", err)
	}
	if _, err := (Source{Getenv: env(map[string]string{EnvKey: string(b)})}).Load(); err != nil {
		t.Fatal(err)
	}
	if _, err := (Source{Getenv: env(map[string]string{EnvIdentityFile: idf})}).Load(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(idf, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := (Source{File: idf}).Load(); err == nil || !strings.Contains(err.Error(), "accessible by group or others") {
		t.Fatalf("got %v", err)
	}
	if _, err := (Source{Getenv: env(map[string]string{EnvKey: "garbage"})}).Load(); err == nil {
		t.Fatal("garbage identity accepted")
	}
}

func TestInvalidNamesAreRejected(t *testing.T) {
	st, _, _ := newStore(t)
	for _, n := range []string{"", "../x", "a/b", ".hidden"} {
		if _, err := st.Decrypt(n); err == nil {
			t.Errorf("%q accepted", n)
		}
	}
}

func TestShortValuesAreNotRedacted(t *testing.T) {
	r := NewRedactor()
	r.Add([]byte("ab"))
	if r.String("abab") != "abab" {
		t.Fatal("a 2 byte value must not mask unrelated text")
	}
}
