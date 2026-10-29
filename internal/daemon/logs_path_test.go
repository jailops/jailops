package daemon_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"jailops.net/jailops/internal/lifecycle"
	"jailops.net/jailops/internal/priv"
	"jailops.net/jailops/internal/service"
	"jailops.net/jailops/internal/sys/zfs"
	"jailops.net/jailops/sdk/api"
)

// The paths the service asks the helper for are built from the lifecycle
// layout (RootMP) and service.JailLogDir; the helper's allow-list must accept
// exactly those. Tests of the service use an in-memory reader, so this is
// the one place where the real path meets the real check.
func TestJailLogPathsPassTheHelper(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mountBase := filepath.Join(dir, "jailops")
	cfg := priv.Config{StorageRoots: []string{"tank/jailops"}, StorageFile: filepath.Join(dir, "priv.d", "storage.conf"),
		MountBase: mountBase, ConfDir: filepath.Join(dir, "etc", "jails"), StateDir: filepath.Join(dir, "jailops", "state"),
		HelperPath: "/usr/local/libexec/jailops-priv"}
	for _, d := range []string{cfg.MountBase, cfg.ConfDir, cfg.StateDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	z := zfs.NewFake("tank")
	if err := z.Create(context.Background(), zfs.CreateOpts{Name: "tank/jailops", Props: map[string]string{"jailops:managed": "on", "mountpoint": mountBase}}); err != nil {
		t.Fatal(err)
	}
	l, err := priv.NewLocal(cfg, priv.Deps{ZFS: z}, priv.WithoutChown())
	if err != nil {
		t.Fatal(err)
	}
	// what the service computes for jail web of project alpha
	lc := lifecycle.Config{Root: "tank/jailops", MountBase: mountBase}
	root := lc.RootMP("alpha_web")
	logDir := service.JailLogDir(root)
	if err := os.MkdirAll(filepath.Join(logDir, "nginx"), 0o755); err != nil {
		t.Fatal(err)
	}
	for p, c := range map[string]string{"messages": "a\nb\n", "nginx/error.log": "e\n"} {
		if err := os.WriteFile(filepath.Join(logDir, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var reader service.LogReader = l
	files, _, err := reader.ListLogs(context.Background(), logDir)
	if err != nil || len(files) != 2 || files[0].Name != "messages" || files[1].Name != "nginx/error.log" {
		t.Fatalf("the helper refused the service's log directory: %v %+v", err, files)
	}
	for _, f := range files {
		data, start, size, err := reader.TailFile(context.Background(), filepath.Join(logDir, f.Name), 0, api.MaxLogReadBytes)
		if err != nil || start != 0 || size != f.Size || int64(len(data)) != f.Size {
			t.Fatalf("%s: the helper refused the service's file path: %v", f.Name, err)
		}
	}
	// the same path for an unmounted jail carries the marker the service turns into "not mounted"
	_, _, err = reader.ListLogs(context.Background(), service.JailLogDir(lc.RootMP("alpha_db")))
	var ae *api.Error
	if !api.IsCode(err, api.CodeNotFound) || !errorsAs(err, &ae) || ae.Detail["missing"] != "root" {
		t.Fatalf("unmounted jail: %v", err)
	}
}

func errorsAs(err error, target **api.Error) bool {
	ae, ok := err.(*api.Error)
	*target = ae
	return ok
}
