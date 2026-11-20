package server_test

import (
	"bytes"
	"strings"
	"testing"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/internal/jailxfer"
	"jailops.net/jailops/sdk/api"
)

const sampleNotes = "# web\n\nServes the shop.\n\n- owner: ops team\n- restart: `service nginx restart`\n\tindented"

// Notes are stored with the jail, changed under If-Match like any update, refused when they are not text, and carried by
// an export into the imported jail.
func TestJailNotesRoundTrip(t *testing.T) {
	e := dtest.New(t, dtest.Options{})
	e.Seed()
	c := e.Admin
	ctx := t.Context()
	j := dtest.WebJail("web")
	j.Spec.Notes = sampleNotes
	op, err := c.Jails().Create(ctx, j)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := op.Wait(ctx); err != nil {
		t.Fatalf("%v\n%s", err, e.Log())
	}
	got, etag, err := c.Jails().GetETag(ctx, "web")
	if err != nil || got.Spec.Notes != sampleNotes {
		t.Fatalf("notes after create: %q %v", got.Spec.Notes, err)
	}

	// a change of the notes is a new generation; the version it was made from goes stale
	gen := got.Metadata.Generation
	got.Spec.Notes = "short"
	upd, err := c.Jails().Update(ctx, "web", got, etag)
	if err != nil {
		t.Fatal(err)
	}
	if upd.Spec.Notes != "short" || upd.Metadata.Generation != gen+1 {
		t.Fatalf("after the update: notes %q generation %d (was %d)", upd.Spec.Notes, upd.Metadata.Generation, gen)
	}
	got.Spec.Notes = "lost"
	if _, err := c.Jails().Update(ctx, "web", got, etag); !api.IsCode(err, api.CodePreconditionFailed) {
		t.Fatalf("a stale version: %v", err)
	}

	// what is not text is refused, and so is a text beyond the limit
	cur, etag, _ := c.Jails().GetETag(ctx, "web")
	for name, bad := range map[string]string{
		"carriage return": "a\r\nb",
		"escape":          "a\x1b[31mb",
		"too long":        strings.Repeat("x", api.MaxNotes+1),
	} {
		next := cur
		next.Spec.Notes = bad
		if _, err := c.Jails().Update(ctx, "web", next, etag); !api.IsCode(err, api.CodeInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}

	// export and import carry the notes
	cur.Spec.Notes = sampleNotes
	if _, err := c.Jails().Update(ctx, "web", cur, etag); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err := c.Jails().Export(ctx, "web", api.JailExportRequest{}, &b); err != nil {
		t.Fatal(err)
	}
	xr, err := jailxfer.NewReader(bytes.NewReader(b.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if xr.Manifest().Bundle.Spec.Notes != sampleNotes {
		t.Errorf("the export file has notes %q", xr.Manifest().Bundle.Spec.Notes)
	}
	res, err := c.Jails().Import(ctx, api.JailImportRequest{As: "web2"}, bytes.NewReader(b.Bytes()))
	if err != nil {
		t.Fatalf("%v\n%s", err, e.Log())
	}
	if res.Jail.Spec.Notes != sampleNotes {
		t.Errorf("the imported jail has notes %q", res.Jail.Spec.Notes)
	}
}

// The other kinds that have notes validate them with the same rule.
func TestNotesOfOtherKinds(t *testing.T) {
	e := dtest.New(t, dtest.Options{})
	e.Seed()
	ctx := t.Context()
	n, etag, err := e.Admin.Networks().GetETag(ctx, "lan")
	if err != nil {
		t.Fatal(err)
	}
	n.Spec.Notes = sampleNotes
	upd, err := e.Admin.Networks().Update(ctx, "lan", n, etag)
	if err != nil || upd.Spec.Notes != sampleNotes {
		t.Fatalf("network notes: %q %v", upd.Spec.Notes, err)
	}
	upd.Spec.Notes = "a\x00b"
	if _, err := e.Admin.Networks().Update(ctx, "lan", upd, ""); !api.IsCode(err, api.CodeInvalid) {
		t.Errorf("a NUL in the notes of a network: %v", err)
	}
}
