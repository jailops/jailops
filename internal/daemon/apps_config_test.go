package daemon_test

import (
	"strings"
	"testing"

	"jailops.net/jailops/internal/apps"
	"jailops.net/jailops/internal/daemon"
)

// apps.catalog_dir defaults to the package's directory, must be absolute,
// and an empty value means the default.
func TestAppsCatalogDirConfig(t *testing.T) {
	if apps.DefaultDir != "/usr/local/share/jailops/apps" {
		t.Fatalf("the default directory moved: %s", apps.DefaultDir)
	}
	cfg, err := daemon.Decode(strings.NewReader("pool: tank\n"))
	if err != nil || cfg.Apps.CatalogDir != apps.DefaultDir {
		t.Fatalf("%v %q", err, cfg.Apps.CatalogDir)
	}
	cfg, err = daemon.Decode(strings.NewReader("pool: tank\napps:\n  catalog_dir: \"\"\n"))
	if err != nil || cfg.Apps.CatalogDir != apps.DefaultDir {
		t.Fatalf("%v %q", err, cfg.Apps.CatalogDir)
	}
	cfg, err = daemon.Decode(strings.NewReader("pool: tank\napps:\n  catalog_dir: /srv/apps\n"))
	if err != nil || cfg.Apps.CatalogDir != "/srv/apps" {
		t.Fatalf("%v %q", err, cfg.Apps.CatalogDir)
	}
	if _, err := daemon.Decode(strings.NewReader("pool: tank\napps:\n  catalog_dir: apps\n")); err == nil || !strings.Contains(err.Error(), "apps.catalog_dir must be an absolute path") {
		t.Fatalf("%v", err)
	}
	if _, err := daemon.Decode(strings.NewReader("pool: tank\napps:\n  dir: /x\n")); err == nil || !strings.Contains(err.Error(), "dir") {
		t.Fatalf("an unknown key was accepted: %v", err)
	}
}
