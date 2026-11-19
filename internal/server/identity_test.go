package server_test

import (
	"testing"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

func makeJail(t *testing.T, e *dtest.Env, name string) api.Jail {
	t.Helper()
	c := ctxT(t)
	op, err := e.Admin.Jails().Create(c, dtest.WebJail(name))
	if err != nil {
		t.Fatal(err)
	}
	j, err := op.Wait(c)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func dropJail(t *testing.T, e *dtest.Env, name string) {
	t.Helper()
	c := ctxT(t)
	op, err := e.Admin.Jails().Delete(c, name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := op.Wait(c); err != nil {
		t.Fatal(err)
	}
}

// A jail created again under the same name starts at generation 1 with a new UUID. The ETag carries both, so a change
// or delete sent with the tag of the old jail fails although the generation matches.
func TestETagNamesTheIncarnation(t *testing.T) {
	e := seeded(t)
	c := ctxT(t)
	old := makeJail(t, e, "web")
	dropJail(t, e, "web")
	fresh := makeJail(t, e, "web")
	if fresh.Metadata.UUID == old.Metadata.UUID || fresh.Metadata.Generation != old.Metadata.Generation {
		t.Fatalf("the test needs a new incarnation with the same generation: %s/%d then %s/%d",
			old.Metadata.UUID, old.Metadata.Generation, fresh.Metadata.UUID, fresh.Metadata.Generation)
	}
	cur, etag, err := e.Admin.Jails().GetETag(c, "web")
	if err != nil {
		t.Fatal(err)
	}
	if etag != api.ETag(fresh.Metadata.UUID, fresh.Metadata.Generation) {
		t.Fatalf("the ETag is %s, want the UUID and generation of the jail", etag)
	}
	stale := api.ETag(old.Metadata.UUID, old.Metadata.Generation)
	cur.Spec.Resources.Disk = "5G"
	if _, err := e.Admin.Jails().Update(c, "web", cur, stale); !api.IsCode(err, api.CodePreconditionFailed) {
		t.Fatalf("an update with the tag of the old incarnation must conflict: %v", err)
	}
	if _, err := e.Admin.Jails().DeleteETag(c, "web", stale); !api.IsCode(err, api.CodePreconditionFailed) {
		t.Fatalf("a delete with the tag of the old incarnation must conflict: %v", err)
	}
	if _, err := e.Admin.Jails().Get(c, "web"); err != nil {
		t.Fatalf("the new jail must still be there: %v", err)
	}
	if _, err := e.Admin.Jails().Update(c, "web", cur, etag); err != nil {
		t.Fatalf("an update with the current tag: %v", err)
	}
	// a tag of another generation is refused, a tag this server did not write too
	cur, etag, _ = e.Admin.Jails().GetETag(c, "web")
	for _, bad := range []string{api.ETag(cur.Metadata.UUID, cur.Metadata.Generation-1), `"1"`, `"web"`} {
		if _, err := e.Admin.Jails().DeleteETag(c, "web", bad); !api.IsCode(err, api.CodePreconditionFailed) {
			t.Fatalf("a delete with %s must conflict: %v", bad, err)
		}
	}
	op, err := e.Admin.Jails().DeleteETag(c, "web", etag)
	if err != nil {
		t.Fatalf("a delete with the current tag: %v", err)
	}
	if _, err := op.Wait(c); err != nil {
		t.Fatal(err)
	}
}

// A create repeated with the same Idempotency-Key and body returns the operation of the first one.
func TestRetriedCreateReturnsTheSameOperation(t *testing.T) {
	e := seeded(t)
	c := sdk.WithIdempotencyKey(ctxT(t), "create-web-0001")
	j := dtest.WebJail("web")
	first, err := e.Admin.Jails().Create(c, j)
	if err != nil {
		t.Fatal(err)
	}
	again, err := e.Admin.Jails().Create(c, j)
	if err != nil {
		t.Fatalf("the repeat must return the first operation, not conflict with the jail it created: %v", err)
	}
	if first.ID() != again.ID() {
		t.Fatalf("the repeat started another operation: %s then %s", first.ID(), again.ID())
	}
	if _, err := again.Wait(c); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, err := range e.Admin.Jails().List(c) {
		if err != nil {
			t.Fatal(err)
		}
		n++
	}
	if n != 1 {
		t.Fatalf("%d jails after a repeated create", n)
	}
	// the same key for another body is refused
	other := dtest.WebJail("other")
	if _, err := e.Admin.Jails().Create(c, other); !api.IsCode(err, api.CodePreconditionFailed) {
		t.Fatalf("a key reused for another body must conflict: %v", err)
	}
}
