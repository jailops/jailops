package cli

import (
	"bytes"
	"strings"
	"testing"
)

func passwordApp(answers ...string) (*app, *bytes.Buffer) {
	var errb bytes.Buffer
	e := &Env{Stderr: &errb}
	e.ReadSecret = func(string) (string, error) {
		v := answers[0]
		answers = answers[1:]
		return v, nil
	}
	return &app{env: e}, &errb
}

// A password that is empty or does not match the repeat is asked again, up to three times, with the reason said.
func TestNewPasswordAsksAgain(t *testing.T) {
	a, errb := passwordApp("", "one", "two", "good", "good")
	pw, err := a.newPassword("")
	if err != nil || pw != "good" {
		t.Fatalf("password %q, error %v", pw, err)
	}
	if !strings.Contains(errb.String(), "the password is empty") || !strings.Contains(errb.String(), "the passwords do not match") {
		t.Fatalf("the reasons were not said: %q", errb.String())
	}
	a, _ = passwordApp("a", "b", "a", "b", "a", "b")
	if _, err := a.newPassword(""); err == nil || err.Error() != "the passwords do not match" {
		t.Fatalf("after three tries: %v", err)
	}
	a, _ = passwordApp("", "", "")
	if _, err := a.newPassword(""); err == nil || err.Error() != "the password is empty" {
		t.Fatalf("empty: %v", err)
	}
}
