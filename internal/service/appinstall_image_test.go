package service

import (
	"errors"
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
)

// Without --image an app is installed from the only FreeBSD image with the
// alias stable, whatever its name; none or several is refused with the images
// there are.
func TestDefaultAppImage(t *testing.T) {
	s := newSvc(t)
	ctx := WithActor(bg, Actor{Name: "root", Kind: "unix", Perm: api.PermAdmin, Global: true})
	add := func(name string, os api.OS, aliases ...string) {
		t.Helper()
		if _, err := s.imgKind.create(ctx, api.ObjectMeta{Name: name}, api.ImageSpec{OS: os, Release: "15.1-RELEASE", Aliases: aliases}); err != nil {
			t.Fatal(err)
		}
	}
	refused := func(msg, hint string) {
		t.Helper()
		_, err := s.defaultAppImage(ctx)
		var ae *api.Error
		if !errors.As(err, &ae) || ae.Code != api.CodeInvalid || !strings.Contains(ae.Message, msg) || !strings.Contains(ae.Hint, hint) {
			t.Fatalf("want invalid %q with hint %q, got %v", msg, hint, err)
		}
	}
	refused("no FreeBSD image has the alias stable", "name an image with --image, for example freebsd-15@stable; you have no images")
	add("alp", api.OSLinux, "stable")
	add("old", api.OSFreeBSD, "lts")
	refused("no FreeBSD image has the alias stable", "for example freebsd-15@stable; your images: alp, old")
	add("freebsd-15", api.OSFreeBSD, "stable")
	if got, err := s.defaultAppImage(ctx); err != nil || got != "freebsd-15@stable" {
		t.Fatalf("%q %v", got, err)
	}
	add("freebsd", api.OSFreeBSD, "stable", "latest")
	refused("2 FreeBSD images have the alias stable (freebsd, freebsd-15)", "for example freebsd@stable; your images: alp, freebsd, freebsd-15, old")
	if err := s.imgKind.delete(ctx, "freebsd-15", AnyGeneration); err != nil {
		t.Fatal(err)
	}
	if got, err := s.defaultAppImage(ctx); err != nil || got != "freebsd@stable" {
		t.Fatalf("an image named freebsd keeps freebsd@stable: %q %v", got, err)
	}
}
