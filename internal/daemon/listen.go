package daemon

import (
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

func (d *Daemon) listen() error {
	cfg := d.cfg
	if p := cfg.Listen.Unix.Path; p != "" {
		ln, err := listenUnix(p, os.FileMode(cfg.Listen.Unix.Mode))
		if err != nil {
			return err
		}
		d.unixLn = ln
		d.serve(ln, "unix", nil)
	}
	if a := cfg.Listen.HTTPS.Address; a != "" {
		tc := &tls.Config{
			GetCertificate: d.certs.GetCertificate, MinVersion: tls.VersionTLS13,
			// Client certificates are optional: a pinned one authenticates a
			// host or hub, everybody else uses a token or the UI login.
			ClientAuth: tls.RequestClientCert,
			NextProtos: []string{"h2", "http/1.1"},
		}
		ln, err := tls.Listen("tcp", a, tc)
		if err != nil {
			return fmt.Errorf("daemon: listening on %s: %w", a, err)
		}
		d.httpsLn = ln
		d.serve(ln, "https", tc)
	}
	return nil
}

func (d *Daemon) serve(ln net.Listener, name string, _ *tls.Config) {
	hs := &http.Server{
		Handler:           d.srv.Handler(),
		ConnContext:       d.srv.ConnContext,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(d.log.Handler(), slog.LevelWarn),
	}
	d.servers = append(d.servers, hs)
	d.go_(func() {
		if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			d.log.Error("listener failed", "listener", name, "err", err)
			select {
			case d.failed <- fmt.Errorf("%s listener: %w", name, err):
			default:
			}
		}
	})
}

// listenUnix creates the socket. A socket nobody listens on is replaced; a
// live one means another daemon runs.
func listenUnix(path string, mode os.FileMode) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(path); err == nil {
		if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
			c.Close()
			return nil, fmt.Errorf("daemon: %s is in use: another jailopsd is running", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if mode == 0 {
		mode = 0o666
	}
	if err := os.Chmod(path, mode); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}
