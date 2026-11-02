package daemon_test

import (
	"bufio"
	"context"
	"io"
	"maps"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"jailops.net/jailops/internal/lifecycle"
	"jailops.net/jailops/internal/logfwd"
	"jailops.net/jailops/internal/priv"
	"jailops.net/jailops/internal/sys/zfs"
	"jailops.net/jailops/sdk/api"
)

type offsets struct {
	mu sync.Mutex
	m  map[string]map[string]int64
}

func (o *offsets) Load(_ context.Context, s string) (map[string]int64, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return maps.Clone(o.m[s]), nil
}
func (o *offsets) Save(_ context.Context, s string, m map[string]int64) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.m[s] = maps.Clone(m)
	return nil
}
func (o *offsets) Delete(_ context.Context, s string) error { return nil }

// The forwarder needs nothing of the helper beyond fs.tail_file, with the
// paths the service builds: a sink reads a jail's messages through the real
// helper checks and sends it; a path outside a jail's log directory is
// refused by the helper whatever the forwarder asks.
func TestForwarderReadsThroughTheHelper(t *testing.T) {
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
	lc := lifecycle.Config{Root: "tank/jailops", MountBase: mountBase}
	logDir := filepath.Join(lc.RootMP("alpha_web"), "var", "log")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "messages"), []byte("Oct 29 11:00:00 web sshd[1]: through the helper\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := l.TailFile(context.Background(), filepath.Join(lc.RootMP("alpha_web"), "etc", "master.passwd"), 0, 1); !api.IsCode(err, api.CodeForbidden) {
		t.Fatalf("the helper read outside the log directory: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.SetReadDeadline(time.Now().Add(30 * time.Second))
		r := bufio.NewReader(c)
		ls, _ := r.ReadString(' ')
		n, _ := strconv.Atoi(strings.TrimSpace(ls))
		b := make([]byte, n)
		_, _ = io.ReadFull(r, b)
		got <- string(b)
	}()
	off := &offsets{m: map[string]map[string]int64{"s": {"alpha/web/messages": 0}}}
	f := logfwd.New(logfwd.Env{Hostname: "host1", Reader: l, Offsets: off, Poll: 5 * time.Millisecond,
		Jails: func(context.Context) ([]logfwd.Jail, error) {
			return []logfwd.Jail{{Project: "alpha", Name: "web", LogDir: logDir}}, nil
		}})
	f.Apply([]api.LogSink{{Name: "s", Type: api.LogSinkSyslog, To: "tcp://" + ln.Addr().String()}})
	ctx, cancel := context.WithCancel(context.Background())
	f.Start(ctx)
	defer func() { cancel(); f.Wait() }()
	if s := <-got; !strings.Contains(s, `jail="web"`) || !strings.Contains(s, "through the helper") {
		t.Fatalf("%q", s)
	}
}
