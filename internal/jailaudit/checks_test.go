package jailaudit

import (
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
)

// freshJail is what the helper reads from a jail of a current image: the defaults of the image, the JailOps block of
// /etc/hosts, the prompt in /root/.shrc.
func freshJail() Input {
	return Input{
		Facts: Facts{
			Accounts:   Accounts{Read: true, Root: PassLocked, Toor: PassLocked, RootShell: "/bin/sh"},
			ShrcPrompt: true,
			Verified:   1200,
			Texts: map[string]string{
				"/etc/rc.conf":       "sendmail_enable=\"NONE\"\nsyslogd_flags=\"-ss\"\ncron_flags=\"-J 60\"\n",
				"/etc/rc.conf.local": "hostname=\"web\"\n",
				"/etc/periodic.conf": "daily_status_world_kernel=\"NO\"\ndaily_output=\"/var/log/daily.log\"\nweekly_output=\"/var/log/weekly.log\"\nmonthly_output=\"/var/log/monthly.log\"\ndaily_status_security_output=\"/var/log/security\"\n",
				"/etc/hosts":         "127.0.0.1 localhost\n# BEGIN JailOps (the name of this jail and its addresses; edit outside this block)\n10.0.0.5\tweb\n# END JailOps\n",
				// FreeBSD 16 ships FreeBSD-base enabled in /etc/pkg; the file of the image turns it off
				"/etc/pkg/FreeBSD.conf": `FreeBSD-ports: {
  url: "pkg+https://pkg.FreeBSD.org/${ABI}/quarterly",
  mirror_type: "srv",
  enabled: yes
}
FreeBSD-base: {
  url: "pkg+https://pkg.FreeBSD.org/${ABI}/base_latest",
  enabled: yes
}
`,
				"/usr/local/etc/pkg/repos/FreeBSD-base.conf": "# Written by JailOps\nFreeBSD-base: { enabled: no }\n",
				"/usr/local/etc/pkg/repos/FreeBSD.conf":      "FreeBSD-ports-kmods: { enabled: no }\n",
				"/usr/local/etc/pkg/repos/jailops.conf":      "jailops: {\n  url: \"https://pkg.example.net/jailops\",\n  signature_type: \"pubkey\"\n}\n",
			},
		},
		HasPkgDB: true, Packages: 52, BasePackages: 50, UsedBytes: 70 << 20, DiskLimit: "10G", Running: true,
		Hostname: "web", Addresses: []string{"10.0.0.5"},
		Listeners: []Listener{{Command: "nginx", Proto: "tcp", Port: 80}, {Command: "syslogd", Proto: "udp", Port: 514, Loopback: true}},
		Exposed:   []string{"tcp/80", "tcp/8000-8100"}, JailopsRepoURL: "https://pkg.example.net/jailops",
	}
}

func results(in Input) map[string]api.JailAuditCheck {
	out := map[string]api.JailAuditCheck{}
	for _, c := range Evaluate(in) {
		out[c.ID] = c
	}
	return out
}

func TestFreshJailPasses(t *testing.T) {
	for id, c := range results(freshJail()) {
		want := api.AuditPass
		if id == "disk.size" {
			want = api.AuditInfo
		}
		if c.Result != want {
			t.Errorf("%s: %s %s", id, c.Result, c.Detail)
		}
		if c.Result == api.AuditPass && c.Fix != "" {
			t.Errorf("%s passes with a fix", id)
		}
	}
}

// Only a security check is ever critical, and every finding names its fix.
func TestEveryFindingHasAFixAndOnlySecurityIsCritical(t *testing.T) {
	in := Input{Facts: Facts{Suspected: []string{"etc/ssl is a symbolic link out of the root (to /tmp/x)"},
		Accounts: Accounts{Read: true, Root: PassEmpty, Toor: PassSet, UID0: []string{"backdoor"}, Empty: []string{"guest"}, RootShell: "/bin/csh"},
		SetID:    []string{"/tmp/x"}, Modified: []string{"/bin/ls"}, Verified: 10, SSHD: true,
		Mounts: []Mount{{Path: "/srv/data", NoSUID: false}},
		Texts: map[string]string{
			"/etc/rc.conf":                          "ntpd_enable=\"YES\"\ndumpdev=\"AUTO\"\nsendmail_enable=\"YES\"\npowerd_enable=NO\n",
			"/etc/periodic.conf":                    "daily_status_world_kernel=\"YES\"\n",
			"/etc/ssh/sshd_config":                  "PasswordAuthentication yes\nPermitRootLogin yes\n",
			"/etc/pkg/FreeBSD.conf":                 "FreeBSD-ports: { url: \"pkg+https://pkg.FreeBSD.org/${ABI}/quarterly\" }\nFreeBSD-base: { url: \"pkg+https://pkg.FreeBSD.org/x\", enabled: yes }\n",
			"/usr/local/etc/pkg/repos/FreeBSD.conf": "FreeBSD-ports: { url: \"http://mirror.example.org/x\" }\n",
		}},
		HasPkgDB: true, Running: true, Hostname: "web", Addresses: []string{"10.0.0.5"}, Shell: "/bin/sh",
		Listeners: []Listener{{Command: "postgres", Proto: "tcp", Port: 5432}}}
	got := results(in)
	want := map[string]api.JailAuditResult{"root.links": api.AuditCritical, "accounts.uid0": api.AuditCritical, "accounts.passwords": api.AuditWarn,
		"files.setid": api.AuditWarn, "pkg.repositories": api.AuditWarn, "pkg.base_repository": api.AuditWarn, "base.checksums": api.AuditWarn,
		"sshd.config": api.AuditWarn, "periodic.world_kernel": api.AuditInfo, "periodic.output": api.AuditInfo, "cron.jitter": api.AuditInfo,
		"rc.conf": api.AuditInfo, "shell.prompt": api.AuditInfo, "hosts.block": api.AuditInfo, "volumes.nosuid": api.AuditWarn,
		"disk.limit": api.AuditInfo, "ports.listening": api.AuditInfo}
	for id, r := range want {
		c := got[id]
		if c.Result != r {
			t.Errorf("%s: %s, want %s (%s)", id, c.Result, r, c.Detail)
		}
		if c.Fix == "" {
			t.Errorf("%s has no fix", id)
		}
		if c.Result == api.AuditCritical && c.Group != GroupSecurity {
			t.Errorf("%s is a practice check and critical", id)
		}
	}
	for id, sub := range map[string]string{"pkg.repositories": "FreeBSD-ports (http://mirror.example.org/x, in /usr/local/etc/pkg/repos/FreeBSD.conf)",
		"rc.conf": `dumpdev="AUTO", ntpd_enable="YES"`, "sshd.config": "PermitRootLogin yes and PasswordAuthentication yes",
		"accounts.passwords": "root has an empty password", "ports.listening": "tcp/5432 (postgres)"} {
		if !strings.Contains(got[id].Detail, sub) {
			t.Errorf("%s: %q lacks %q", id, got[id].Detail, sub)
		}
	}
	if strings.Contains(got["rc.conf"].Detail, "powerd") {
		t.Errorf("powerd_enable=NO named: %s", got["rc.conf"].Detail)
	}
}

func TestSetIDOfAPackageIsNotAFinding(t *testing.T) {
	in := freshJail()
	in.Facts.SetID = []string{"/usr/local/bin/sudo"}
	in.Owned = map[string]bool{"/usr/local/bin/sudo": true}
	if c := results(in)["files.setid"]; c.Result != api.AuditPass {
		t.Fatalf("%+v", c)
	}
	in.HasPkgDB = false
	if c := results(in)["files.setid"]; c.Result != api.AuditInfo {
		t.Fatalf("without a package database: %+v", c)
	}
}

func TestStoppedJail(t *testing.T) {
	in := freshJail()
	in.Running, in.Listeners = false, nil
	in.Facts.Mounts = nil
	got := results(in)
	if got["volumes.nosuid"].Result != api.AuditPass || got["ports.listening"].Result != api.AuditInfo {
		t.Fatalf("%+v %+v", got["volumes.nosuid"], got["ports.listening"])
	}
}

func TestSSHDTakesTheFirstValueBeforeMatch(t *testing.T) {
	f := Facts{SSHD: true, Texts: map[string]string{"/etc/ssh/sshd_config": "PermitRootLogin prohibit-password\nPermitRootLogin yes\nKbdInteractiveAuthentication no\nMatch User x\n  PasswordAuthentication yes\n"}}
	if c := checkSSHD(f); c.Result != api.AuditPass {
		t.Fatalf("%+v", c)
	}
}

// The stock sshd_config of FreeBSD (every line a comment) takes passwords through keyboard-interactive and PAM (sshd -T:
// usepam yes, kbdinteractiveauthentication yes); so does UsePAM yes with ChallengeResponseAuthentication yes. Either
// switch off is no password.
func TestSSHDKeyboardInteractiveThroughPAMIsAPassword(t *testing.T) {
	for conf, want := range map[string]api.JailAuditResult{
		"#PasswordAuthentication no\n#KbdInteractiveAuthentication yes\n#UsePAM yes\n": api.AuditWarn,
		"UsePAM yes\nChallengeResponseAuthentication yes\n":                            api.AuditWarn,
		"KbdInteractiveAuthentication no\n":                                            api.AuditPass,
		"UsePAM no\n":                                                                  api.AuditPass,
		"ChallengeResponseAuthentication no\n":                                         api.AuditPass,
	} {
		c := checkSSHD(Facts{SSHD: true, Texts: map[string]string{"/etc/ssh/sshd_config": conf}})
		if c.Result != want || want == api.AuditWarn && !strings.Contains(c.Detail, "KbdInteractiveAuthentication") {
			t.Errorf("%q: %+v", conf, c)
		}
	}
}

func TestRepoURL(t *testing.T) {
	if u := RepoURL("# c\njailops: {\n  url: \"https://pkg.example.net/x\", // the mirror\n  enabled: yes\n}\n", "jailops"); u != "https://pkg.example.net/x" {
		t.Fatalf("%q", u)
	}
}
