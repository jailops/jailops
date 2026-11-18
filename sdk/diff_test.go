package sdk_test

import (
	"testing"

	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

func TestJailDiffAgainstFake(t *testing.T) {
	srv, c := newEnv(t)
	seedBasics(srv)
	mustJail(t, c, "web", webSpec())
	ctx := ctxT(t)
	r, err := c.Jails().Diff(ctx, "web", sdk.DiffOptions{})
	if err != nil || r.Jail != "web" || len(r.Datasets) != 9 || r.Datasets[0].Name != "root" || r.Total() != 2 {
		t.Fatalf("%v %+v", err, r)
	}
	r, err = c.Jails().Diff(ctx, "web", sdk.DiffOptions{Dataset: "etc", Packages: true})
	if err != nil || len(r.Datasets) != 1 || r.Datasets[0].Entries[0] != (api.DiffEntry{Kind: api.DiffModified, Path: "/etc/rc.conf"}) || r.Packages == nil {
		t.Fatalf("%v %+v", err, r)
	}
	if _, err := c.Jails().Diff(ctx, "web", sdk.DiffOptions{Dataset: "nope"}); !api.IsCode(err, api.CodeInvalid) {
		t.Errorf("unknown dataset: %v", err)
	}
	if _, err := c.Jails().Diff(ctx, "nobody", sdk.DiffOptions{}); !api.IsCode(err, api.CodeNotFound) {
		t.Errorf("unknown jail: %v", err)
	}
}
