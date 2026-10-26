package server_test

import (
	"bytes"
	"encoding/base64"
	"io"
	"strings"
	"testing"

	"filippo.io/age"
	"filippo.io/age/armor"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

func sealTo(t *testing.T, recipient, secret string) string {
	return sealToAs(t, recipient, secret, false)
}

// sealToAs seals like the fleet tool does (armored) or as a plain binary age file.
func sealToAs(t *testing.T, recipient, secret string, armored bool) string {
	t.Helper()
	r, err := age.ParseX25519Recipient(recipient)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	var out io.Writer = &buf
	var aw io.WriteCloser
	if armored {
		aw = armor.NewWriter(&buf)
		out = aw
	}
	w, err := age.Encrypt(out, r)
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte(secret))
	w.Close()
	if aw != nil {
		aw.Close()
	}
	return "sealed:age:" + base64.StdEncoding.EncodeToString(buf.Bytes())
}

// A sealed environment value is stored and returned sealed, and opened only for a process the host starts in the jail.
func TestSealedEnvIsOpenedOnlyForExec(t *testing.T) {
	e := seeded(t)
	c := ctxT(t)
	info, err := e.Admin.Info(c)
	if err != nil {
		t.Fatal(err)
	}
	if info.SecretsRecipient == "" {
		t.Fatal("the host publishes no recipient")
	}
	has := false
	for _, x := range info.APIExtensions {
		has = has || x == "sealed_secrets"
	}
	if !has {
		t.Fatal("sealed_secrets is not advertised")
	}
	sealed := sealToAs(t, info.SecretsRecipient, "hunter2", true) // the fleet tool's format
	sp := webSpec()
	sp.Network = []api.NetworkAttach{{Network: "lan"}}
	sp.Env = map[string]string{"DB_PASSWORD": sealed, "MODE": "prod"}
	op, err := e.Admin.Jails().Create(c, sdk.NewJail("sec", sp))
	if err != nil {
		t.Fatal(err)
	}
	j, err := op.Wait(c)
	if err != nil {
		t.Fatalf("%v\n%s", err, e.Log())
	}
	if j.Spec.Env["DB_PASSWORD"] != sealed {
		t.Fatalf("the API returned %q", j.Spec.Env["DB_PASSWORD"])
	}
	if strings.Contains(e.Log(), "hunter2") {
		t.Fatal("the secret reached the log")
	}
	so, _ := e.Admin.Jails().Start(c, "sec")
	if _, err := so.Wait(c); err != nil {
		t.Fatal(err)
	}
	s, err := e.Admin.Jails().Exec(c, "sec", []string{"echo", "x"}, sdk.ExecOptions{Env: []string{"MODE=dev"}})
	if err != nil {
		t.Fatal(err)
	}
	drain(s)
	calls := e.FakeExec.Calls()
	env := strings.Join(calls[len(calls)-1].Opts.Env, "\n")
	if !strings.Contains(env, "DB_PASSWORD=hunter2") {
		t.Fatalf("the process did not get the secret: %q", env)
	}
	if !(strings.Index(env, "MODE=prod") < strings.Index(env, "MODE=dev")) {
		t.Fatalf("a caller's value must come after the jail's so that it wins: %q", env)
	}
}

func TestSealedEnvForAnotherHostIsRefused(t *testing.T) {
	e := seeded(t)
	c := ctxT(t)
	other, _ := age.GenerateX25519Identity()
	sp := webSpec()
	sp.Network = []api.NetworkAttach{{Network: "lan"}}
	sp.Env = map[string]string{"X": sealTo(t, other.Recipient().String(), "nope")}
	if _, err := e.Admin.Jails().Create(c, sdk.NewJail("bad", sp)); !api.IsCode(err, api.CodeInvalid) {
		t.Fatalf("a value sealed to another host was accepted: %v", err)
	}
	// a plain binary age file is accepted as well
	sp.Env = map[string]string{"X": sealToAs(t, mustInfo(t, e).SecretsRecipient, "ok", false)}
	op, err := e.Admin.Jails().Create(c, sdk.NewJail("plain", sp))
	if err != nil {
		t.Fatalf("a binary sealed value was refused: %v", err)
	}
	if _, err := op.Wait(c); err != nil {
		t.Fatal(err)
	}
	sp.Env = map[string]string{"X": "sealed:age:not-base64!"}
	if _, err := e.Admin.Jails().Create(c, sdk.NewJail("bad", sp)); !api.IsCode(err, api.CodeInvalid) {
		t.Fatalf("garbage was accepted: %v", err)
	}
}

func mustInfo(t *testing.T, e *dtest.Env) api.ServerInfo {
	t.Helper()
	i, err := e.Admin.Info(ctxT(t))
	if err != nil {
		t.Fatal(err)
	}
	return i
}
