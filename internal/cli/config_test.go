package cli

import (
	"os"
	"path/filepath"
	"testing"
)

type cfgCase struct {
	user, system string
	env          map[string]string
	remote, host string
}

func resolveCase(t *testing.T, c cfgCase) (*Connection, error) {
	t.Helper()
	dir := t.TempDir()
	userPath := filepath.Join(dir, "user.yaml")
	sysPath := filepath.Join(dir, "system.yaml")
	if c.user != "" {
		if err := os.WriteFile(userPath, []byte(c.user), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if c.system != "" {
		if err := os.WriteFile(sysPath, []byte(c.system), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	env := map[string]string{"JAILOPS_CONFIG": userPath}
	for k, v := range c.env {
		env[k] = v
	}
	e := &Env{Getenv: func(k string) string { return env[k] }, SystemConfig: sysPath, HomeDir: func() string { return dir }}
	e.fill()
	a := &app{env: e, remote: c.remote, host: c.host}
	return a.resolveConnection()
}

const sysCfg = `current: shared
remotes:
  shared:
    url: https://shared.example:8443
    token: sys-token
    host: sys-host
  other:
    url: https://other.example:8443
`

const userCfg = `current: mine
remotes:
  mine:
    url: https://mine.example:8443
    token: user-token
    fingerprint: aa
  shared:
    url: https://user-shared.example:8443
`

func TestConfigPrecedence(t *testing.T) {
	tests := []struct {
		name      string
		c         cfgCase
		url, sock string
		token     string
		host      string
		fp        string
	}{
		{"default is the unix socket", cfgCase{}, "", "/var/run/jailops/jailops.sock", "", "", ""},
		{"system file alone", cfgCase{system: sysCfg}, "https://shared.example:8443", "", "sys-token", "sys-host", ""},
		{"user current beats system current", cfgCase{system: sysCfg, user: userCfg}, "https://mine.example:8443", "", "user-token", "", "aa"},
		{"user entry replaces the system entry of the same name", cfgCase{system: sysCfg, user: userCfg, remote: "shared"}, "https://user-shared.example:8443", "", "", "", ""},
		{"system-only entry stays reachable", cfgCase{system: sysCfg, user: userCfg, remote: "other"}, "https://other.example:8443", "", "", "", ""},
		{"JAILOPS_REMOTE beats the file's current", cfgCase{system: sysCfg, user: userCfg, env: map[string]string{"JAILOPS_REMOTE": "other"}}, "https://other.example:8443", "", "", "", ""},
		{"--remote beats JAILOPS_REMOTE", cfgCase{system: sysCfg, user: userCfg, remote: "mine", env: map[string]string{"JAILOPS_REMOTE": "other"}}, "https://mine.example:8443", "", "user-token", "", "aa"},
		{"JAILOPS_TOKEN beats the file", cfgCase{user: userCfg, env: map[string]string{"JAILOPS_TOKEN": "env-token"}}, "https://mine.example:8443", "", "env-token", "", "aa"},
		{"JAILOPS_HOST beats the file", cfgCase{system: sysCfg, env: map[string]string{"JAILOPS_HOST": "env-host"}}, "https://shared.example:8443", "", "sys-token", "env-host", ""},
		{"--host beats JAILOPS_HOST", cfgCase{system: sysCfg, host: "flag-host", env: map[string]string{"JAILOPS_HOST": "env-host"}}, "https://shared.example:8443", "", "sys-token", "flag-host", ""},
		{"remote local selects the socket", cfgCase{user: userCfg, remote: "local"}, "", "/var/run/jailops/jailops.sock", "", "", ""},
		{"JAILOPS_SOCKET overrides the default socket", cfgCase{env: map[string]string{"JAILOPS_SOCKET": "/tmp/x.sock"}}, "", "/tmp/x.sock", "", "", ""},
		{"JAILOPS_URL without a remote", cfgCase{env: map[string]string{"JAILOPS_URL": "https://adhoc:1", "JAILOPS_TOKEN": "t"}}, "https://adhoc:1", "", "t", "", ""},
		{"--remote may be a URL", cfgCase{user: userCfg, remote: "https://direct:9"}, "https://direct:9", "", "", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, err := resolveCase(t, tc.c)
			if err != nil {
				t.Fatal(err)
			}
			if c.URL != tc.url || c.Socket != tc.sock || c.Token != tc.token || c.Host != tc.host || c.Fingerprint != tc.fp {
				t.Fatalf("got url=%q sock=%q token=%q host=%q fp=%q", c.URL, c.Socket, c.Token, c.Host, c.Fingerprint)
			}
		})
	}
}

func TestConfigErrors(t *testing.T) {
	if _, err := resolveCase(t, cfgCase{user: userCfg, remote: "nope"}); err == nil {
		t.Fatal("unknown remote accepted")
	}
	if _, err := resolveCase(t, cfgCase{user: "remotes: [broken"}); err == nil {
		t.Fatal("broken config accepted")
	}
	if _, err := resolveCase(t, cfgCase{user: "unknown_key: 1\n"}); err == nil {
		t.Fatal("unknown top-level key accepted")
	}
}

func TestTokenFile(t *testing.T) {
	dir := t.TempDir()
	tf := filepath.Join(dir, "tok")
	if err := os.WriteFile(tf, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := resolveCase(t, cfgCase{user: "current: a\nremotes:\n  a:\n    url: https://x:1\n    token_file: " + tf + "\n"})
	if err != nil || c.Token != "from-file" {
		t.Fatalf("%v %+v", err, c)
	}
}
