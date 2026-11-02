package logfwd

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"jailops.net/jailops/sdk/api"
)

// sendTimeout bounds one batch: dialing, the handshake and the write or the HTTP exchange.
const sendTimeout = 10 * time.Second

// sender delivers batches to one sink. A nil error means every entry of
// the batch was handed over: written to the socket (udp, tcp, tls) or
// answered with 2xx (webhook).
type sender interface {
	send(ctx context.Context, batch []Entry) error
	close()
}

// CheckFile is the default check of a CA or token file: a regular file
// reached without a symlink, owned by root and writable by nobody else; a
// secret (the token) must not be readable by others either (0600, or 0640
// with the daemon's group).
func CheckFile(path string, secret bool) error {
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	uid := uint32(1<<32 - 1)
	if sys, ok := st.Sys().(*syscall.Stat_t); ok {
		uid = sys.Uid
	}
	return checkMode(path, st.Mode(), uid, secret)
}

func checkMode(path string, mode fs.FileMode, uid uint32, secret bool) error {
	if mode&fs.ModeSymlink != 0 || !mode.IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	if uid != 0 {
		return fmt.Errorf("%s is not owned by root", path)
	}
	if mode.Perm()&0o022 != 0 {
		return fmt.Errorf("%s is writable by group or others (mode %04o)", path, mode.Perm())
	}
	if secret && mode.Perm()&0o007 != 0 {
		return fmt.Errorf("%s is readable by others (mode %04o): use 0600, or 0640 with the group of the daemon", path, mode.Perm())
	}
	return nil
}

// readChecked reads a file after the check, at most 1 MiB.
func (e *Env) readChecked(path string, secret bool) ([]byte, error) {
	if err := e.CheckFile(path, secret); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, 1<<20))
}

// tlsConfig verifies the receiver against the CA file, or the host's roots.
func (e *Env) tlsConfig(k api.LogSink, host string) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}
	if k.CAFile != "" {
		pem, err := e.readChecked(k.CAFile, false)
		if err != nil {
			return nil, fmt.Errorf("ca_file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("ca_file %s holds no PEM certificate", k.CAFile)
		}
		cfg.RootCAs = pool
	}
	return cfg, nil
}

// newSender builds the sender of a sink; the files it names are checked here.
func newSender(k api.LogSink, env *Env) (sender, error) {
	tr, addr, err := k.SinkTarget()
	if err != nil {
		return nil, err
	}
	host, _, _ := net.SplitHostPort(addr)
	switch k.Type {
	case api.LogSinkSyslog:
		fac, _ := api.SyslogFacility(k.Facility)
		s := &syslogSender{tr: tr, addr: addr, facility: fac, env: env}
		if tr == "tls" {
			if s.tlsConf, err = env.tlsConfig(k, host); err != nil {
				return nil, err
			}
		}
		return s, nil
	case api.LogSinkWebhook:
		cfg, err := env.tlsConfig(k, host)
		if err != nil {
			return nil, err
		}
		if k.TokenFile != "" {
			if _, err := env.token(k.TokenFile); err != nil {
				return nil, err
			}
		}
		t := &http.Transport{TLSClientConfig: cfg, DialContext: env.Dial, TLSHandshakeTimeout: sendTimeout, MaxIdleConns: 2, IdleConnTimeout: time.Minute}
		hc := &http.Client{Transport: t, Timeout: sendTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		return &webhookSender{url: k.To, tokenFile: k.TokenFile, hc: hc, t: t, env: env}, nil
	}
	return nil, fmt.Errorf("unknown sink type %q", k.Type)
}

// token reads the first line of a token file.
func (e *Env) token(path string) (string, error) {
	b, err := e.readChecked(path, true)
	if err != nil {
		return "", fmt.Errorf("token_file: %w", err)
	}
	line, _, _ := strings.Cut(string(b), "\n")
	line = strings.TrimSpace(line)
	if line == "" || strings.ContainsFunc(line, func(r rune) bool { return r < 0x21 || r > 0x7e }) {
		return "", fmt.Errorf("token_file %s: the first line must be the token (printable, no spaces)", path)
	}
	return line, nil
}

// syslogSender writes RFC 5424 messages: one datagram each over udp,
// octet-counted frames ("LEN SP MSG", RFC 6587 and 5425) over tcp and tls.
// The connection is kept and dialled again after an error.
type syslogSender struct {
	tr, addr string
	facility int
	env      *Env
	tlsConf  *tls.Config
	conn     net.Conn
	raw      net.Conn // the tcp connection below conn
}

func (s *syslogSender) close() {
	if s.conn != nil {
		s.conn.Close()
		s.conn, s.raw = nil, nil
	}
}

func (s *syslogSender) dial(ctx context.Context) error {
	network := "tcp"
	if s.tr == "udp" {
		network = "udp"
	}
	c, err := s.env.Dial(ctx, network, s.addr)
	if err != nil {
		return err
	}
	s.raw, s.conn = c, c
	if s.tr == "tls" {
		tc := tls.Client(c, s.tlsConf)
		if err := tc.HandshakeContext(ctx); err != nil {
			c.Close()
			s.conn, s.raw = nil, nil
			return err
		}
		s.conn = tc
	}
	return nil
}

// peerClosed reports whether a stream receiver has closed the connection:
// a receiver sends nothing a sender must read, so end of file or an error
// on a short read means it is gone (bytes, such as TLS session tickets,
// are discarded).
func (s *syslogSender) peerClosed() bool {
	if s.tr == "udp" || s.raw == nil {
		return false
	}
	var buf [512]byte
	for range 8 {
		_ = s.raw.SetReadDeadline(time.Now().Add(time.Millisecond))
		_, err := s.raw.Read(buf[:])
		if err == nil {
			continue
		}
		var ne net.Error
		_ = s.raw.SetReadDeadline(time.Time{})
		return !(errors.As(err, &ne) && ne.Timeout())
	}
	return false
}

func (s *syslogSender) send(ctx context.Context, batch []Entry) error {
	if s.conn != nil && s.peerClosed() {
		s.close()
	}
	if s.conn == nil {
		if err := s.dial(ctx); err != nil {
			return err
		}
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(sendTimeout)
	}
	_ = s.conn.SetWriteDeadline(deadline)
	var err error
	if s.tr == "udp" {
		for _, e := range batch {
			if _, err = s.conn.Write(Format5424(e, s.facility, s.env.Hostname)); err != nil {
				break
			}
		}
	} else {
		var b bytes.Buffer
		for _, e := range batch {
			m := Format5424(e, s.facility, s.env.Hostname)
			b.WriteString(strconv.Itoa(len(m)))
			b.WriteByte(' ')
			b.Write(m)
		}
		_, err = s.conn.Write(b.Bytes())
	}
	if err != nil {
		s.close()
	}
	return err
}

// webhookSender POSTs a batch as JSON lines (application/x-ndjson).
type webhookSender struct {
	url, tokenFile string
	hc             *http.Client
	t              *http.Transport
	env            *Env
}

func (w *webhookSender) close() { w.t.CloseIdleConnections() }

func (w *webhookSender) send(ctx context.Context, batch []Entry) error {
	var b bytes.Buffer
	for _, e := range batch {
		b.Write(FormatJSON(e, w.env.Hostname))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, &b)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-ndjson")
	req.Header.Set("User-Agent", "jailopsd")
	if w.tokenFile != "" {
		tok, err := w.env.token(w.tokenFile) // read at each send: a replaced token is used at once
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := w.hc.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			// the URL's query may hold a secret: name the receiver without it
			return fmt.Errorf("POST %s: %w", redactURL(w.url), ue.Err)
		}
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, Escape(strings.TrimSpace(string(body))))
	}
	return nil
}

// redactURL is the URL without its query, fragment and user information.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "the webhook"
	}
	u.RawQuery, u.Fragment, u.User = "", "", nil
	return u.String()
}
