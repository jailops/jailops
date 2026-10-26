package sdk_test

import (
	"slices"
	"testing"

	"jailops.net/jailops/sdk/api"
)

// Aliases are managed explicitly, a deleted generation moves the aliases that pointed at it, and deleting an image
// removes its generations and aliases.
func TestImageAliasesAndDeletion(t *testing.T) {
	srv, c := newEnv(t)
	seedBasics(srv) // freebsd-15 with g1 and the alias stable
	srv.SeedGeneration("freebsd-15")
	srv.SeedGeneration("freebsd-15")
	ctx := ctxT(t)
	im, err := c.Images().Get(ctx, "freebsd-15")
	if err != nil || im.Status.Aliases["stable"] != 3 {
		t.Fatalf("aliases follow the newest generation: %v %+v", err, im.Status.Aliases)
	}
	// set: explicit generation, then the newest when none is given
	if im, err = c.Images().SetAlias(ctx, "freebsd-15", "pinned", 2); err != nil || im.Status.Aliases["pinned"] != 2 || !slices.Contains(im.Spec.Aliases, "pinned") {
		t.Fatalf("%v %+v", err, im)
	}
	if im, err = c.Images().SetAlias(ctx, "freebsd-15", "pinned", 0); err != nil || im.Status.Aliases["pinned"] != 3 {
		t.Fatalf("%v %+v", err, im.Status.Aliases)
	}
	if _, err = c.Images().SetAlias(ctx, "freebsd-15", "pinned", 9); !api.IsCode(err, api.CodeNotFound) {
		t.Fatalf("%v", err)
	}
	if _, err = c.Images().SetAlias(ctx, "nope", "x", 1); !api.IsCode(err, api.CodeNotFound) {
		t.Fatalf("%v", err)
	}
	// deleting a generation moves its aliases to the newest remaining one
	if _, err = c.Images().SetAlias(ctx, "freebsd-15", "old", 2); err != nil {
		t.Fatal(err)
	}
	op, err := c.Images().DeleteGeneration(ctx, "freebsd-15", 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = op.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if im, _ = c.Images().Get(ctx, "freebsd-15"); im.Status.Aliases["old"] != 3 {
		t.Fatalf("%+v", im.Status.Aliases)
	}
	// delete: from the status and from the spec
	if im, err = c.Images().DeleteAlias(ctx, "freebsd-15", "old"); err != nil || im.Status.Aliases["old"] != 0 || slices.Contains(im.Spec.Aliases, "old") {
		t.Fatalf("%v %+v", err, im)
	}
	if _, err = c.Images().DeleteAlias(ctx, "freebsd-15", "old"); !api.IsCode(err, api.CodeNotFound) {
		t.Fatalf("%v", err)
	}
	// the last generation takes the aliases with it
	for _, n := range []int{3, 1} {
		if op, err = c.Images().DeleteGeneration(ctx, "freebsd-15", n); err != nil {
			t.Fatal(err)
		}
		if _, err = op.Wait(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if im, _ = c.Images().Get(ctx, "freebsd-15"); len(im.Status.Aliases) != 0 {
		t.Fatalf("%+v", im.Status.Aliases)
	}
	// image delete cascades
	srv.SeedGeneration("freebsd-15")
	if err = c.Images().Delete(ctx, "freebsd-15"); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Images().Get(ctx, "freebsd-15"); !api.IsCode(err, api.CodeNotFound) {
		t.Fatalf("%v", err)
	}
}
