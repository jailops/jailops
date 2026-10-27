package backupd_test

import (
	"strings"
	"testing"

	"jailops.net/jailops/internal/backupd"
)

const goodConfig = `
datastore: {kind: directory, directory: /var/backups/jailops}
database: {dsn_file: /usr/local/etc/jailops/backupd.dsn}
`

func TestConfigStrictAndValidated(t *testing.T) {
	c, err := backupd.ParseConfig([]byte(goodConfig))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":39111" || c.AdminSocket != "/var/run/jailops-backupd.sock" || c.Grace != backupd.Duration(24*3600e9) {
		t.Fatalf("%+v", c)
	}
	for name, doc := range map[string]string{
		"unknown key":      goodConfig + "frobnicate: yes\n",
		"nested unknown":   "datastore: {kind: directory, directory: /x, nope: 1}\ndatabase: {dsn: x}\n",
		"no datastore":     "database: {dsn: x}\n",
		"bad kind":         "datastore: {kind: ftp}\ndatabase: {dsn: x}\n",
		"no database":      "datastore: {kind: directory, directory: /x}\n",
		"both dsn":         "datastore: {kind: directory, directory: /x}\ndatabase: {dsn: x, dsn_file: y}\n",
		"grace too short":  goodConfig + "grace: 6h\n",
		"bad duration":     goodConfig + "grace: soon\n",
		"lease too short":  goodConfig + "lease: 10s\n",
		"bad cron":         goodConfig + "schedule: {gc: \"every sunday\"}\n",
		"bad timezone":     goodConfig + "retention: {timezone: Mars/Olympus}\n",
		"s3 incomplete":    "datastore: {kind: s3, s3: {bucket: b}}\ndatabase: {dsn: x}\n",
		"host no scope":    goodConfig + "hosts: [{name: a, fingerprint: " + strings.Repeat("a", 64) + "}]\n",
		"host admin scope": goodConfig + "hosts: [{name: a, fingerprint: " + strings.Repeat("a", 64) + ", scopes: [backup-admin]}]\n",
		"host bad fp":      goodConfig + "hosts: [{name: a, fingerprint: zz, scopes: [backup-push]}]\n",
		"empty":            "",
	} {
		if _, err := backupd.ParseConfig([]byte(doc)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	ok := goodConfig + "schedule: {gc: \"0 4 * * 0\", verify_new: \"0 2 * * *\"}\nretention: {daily: 7, timezone: Europe/Berlin}\nhosts: [{name: a, fingerprint: " + strings.Repeat("A", 64) + ", scopes: [backup-push, backup-pull]}]\n"
	if _, err := backupd.ParseConfig([]byte(ok)); err != nil {
		t.Fatal(err)
	}
}

func FuzzParseConfig(f *testing.F) {
	f.Add(goodConfig)
	f.Add("datastore: {kind: s3, s3: {bucket: b, region: r, credentials_file: /x}}\ndatabase: {dsn: x}\nhosts: [{name: a, fingerprint: " + strings.Repeat("a", 64) + ", scopes: [restore]}]\n")
	f.Add("{{{{")
	f.Fuzz(func(t *testing.T, doc string) {
		c, err := backupd.ParseConfig([]byte(doc))
		if err == nil && (c.Listen == "" || c.Lease == 0 || c.Grace < backupd.Duration(12*3600e9)) {
			t.Fatalf("an accepted configuration without defaults or with a short grace: %+v", c)
		}
	})
}

func TestSampleConfigLoads(t *testing.T) {
	c, err := backupd.LoadConfig("../../packaging/backupd.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if c.Datastore.Kind != "directory" || c.Listen != ":39111" || c.Retention.Daily != 14 {
		t.Fatalf("%+v", c)
	}
}
