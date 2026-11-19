package sdk

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"jailops.net/jailops/sdk/api"
)

// maxConsoleInput is the most input one request carries; the server refuses more.
const maxConsoleInput = 32 << 10

// ConsoleSession is an interactive terminal in a jail opened with
// Jails().Console. It talks the session protocol of the web console: plain
// HTTP requests (open, input, resize, close) and a server-sent event stream for
// the output, so it works wherever HTTP does. The terminal is always a
// pseudo-terminal, so output and error output arrive merged.
//
// Input is carried as UTF-8 text: bytes that are not valid UTF-8 are replaced by
// U+FFFD. Use Jails().Exec for binary input.
type ConsoleSession struct {
	j    *JailClient
	name string
	id   string
	base string

	outR *io.PipeReader
	done chan struct{}
	code int
	err  error

	cancel context.CancelFunc
	wmu    sync.Mutex
	closed bool
}

// Console opens an interactive terminal in a running jail. Only o.Rows, o.Cols,
// o.Env, o.User, o.Shell and o.ShellPath matter; without o.Shell the jail's shell
// runs. Rows and Cols give the initial terminal size. The request fails with an
// ordinary error (not_found, busy, forbidden) before a session exists.
//
// The returned session lives until the command exits, Close is called, or the
// server ends it (idle, too long). ctx bounds only the opening and the output
// stream: cancelling it ends the stream, so keep it alive for the session.
func (j *JailClient) Console(ctx context.Context, name string, o ExecOptions) (*ConsoleSession, error) {
	if err := j.c.need("jail_console"); err != nil {
		return nil, err
	}
	req := api.ExecRequest{Shell: true, ShellPath: o.ShellPath, TTY: true, Rows: o.Rows, Cols: o.Cols, Env: o.Env, User: o.User}
	opened, _, err := send[struct {
		ID string `json:"id"`
	}](ctx, j.c, request{method: http.MethodPost, path: j.r.item(name) + "/console", body: req})
	if err != nil {
		return nil, err
	}
	if opened.ID == "" {
		return nil, api.Errorf(api.CodeInternal, "the server opened a console without an id")
	}
	sctx, cancel := context.WithCancel(ctx)
	outR, outW := io.Pipe()
	s := &ConsoleSession{j: j, name: name, id: opened.ID, base: j.r.item(name) + "/console/" + esc(opened.ID),
		outR: outR, done: make(chan struct{}), cancel: cancel}
	go s.pump(sctx, outW)
	return s, nil
}

// Output is what the terminal prints. It must be read for Wait to return.
func (s *ConsoleSession) Output() io.Reader { return s.outR }

// ID is the server's identifier of the session.
func (s *ConsoleSession) ID() string { return s.id }

// Write sends input to the terminal.
func (s *ConsoleSession) Write(p []byte) (int, error) {
	n := 0
	for len(p) > 0 {
		c := p
		if len(c) > maxConsoleInput {
			c = c[:maxConsoleInput]
			// do not cut a multi-byte character in two
			for len(c) > 0 && !utf8.Valid(c) && !utf8.FullRune(c[max(0, len(c)-utf8.UTFMax):]) {
				c = c[:len(c)-1]
			}
			if len(c) == 0 {
				c = p[:1]
			}
		}
		if err := s.post("/input", map[string]string{"data": strings.ToValidUTF8(string(c), "�")}); err != nil {
			return n, err
		}
		n += len(c)
		p = p[len(c):]
	}
	return n, nil
}

// Resize tells the terminal its new size.
func (s *ConsoleSession) Resize(rows, cols uint16) error {
	return s.post("/resize", map[string]uint16{"rows": rows, "cols": cols})
}

func (s *ConsoleSession) post(suffix string, body any) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if s.closed {
		return api.Errorf(api.CodeBusy, "the console session has ended")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _, err := send[struct{}](ctx, s.j.c, request{method: http.MethodPost, path: s.base + suffix, body: body})
	return err
}

// Wait returns the exit status of the command once it has ended and its output
// was read. A session the server cut off (idle, too long) returns a non-zero
// status; a stream that broke before the exit status returns an error.
func (s *ConsoleSession) Wait() (int, error) {
	<-s.done
	return s.code, s.err
}

// Close ends the session: the server kills the command. It is safe to call after
// the command has exited.
func (s *ConsoleSession) Close() error {
	s.wmu.Lock()
	already := s.closed
	s.closed = true
	s.wmu.Unlock()
	if already {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _, err := send[struct{}](ctx, s.j.c, request{method: http.MethodDelete, path: s.base})
	// let the stream deliver the exit status before it is cut
	select {
	case <-s.done:
	case <-time.After(3 * time.Second):
	}
	s.cancel()
	_ = s.outR.Close()
	if api.IsCode(err, api.CodeNotFound) { // it already ended and was forgotten
		return nil
	}
	return err
}

// Run connects the session to stdin and stdout (either may be nil), waits for the
// command and returns its exit status. When stdin ends, Ctrl-D is sent, which
// ends a shell at an empty prompt.
func (s *ConsoleSession) Run(stdin io.Reader, stdout io.Writer) (int, error) {
	if stdout == nil {
		stdout = io.Discard
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _, _ = io.Copy(stdout, s.Output()) }()
	if stdin != nil {
		go func() {
			if _, err := io.Copy(consoleWriter{s}, stdin); err == nil {
				_, _ = s.Write([]byte{4})
			}
		}()
	}
	wg.Wait()
	return s.Wait()
}

type consoleWriter struct{ s *ConsoleSession }

func (w consoleWriter) Write(p []byte) (int, error) { return w.s.Write(p) }

// pump reads the event stream, reconnecting after a drop with Last-Event-ID,
// until the exit status arrives.
func (s *ConsoleSession) pump(ctx context.Context, out *io.PipeWriter) {
	defer close(s.done)
	defer out.Close()
	var last string
	failing := 0
	for {
		got, exited, err := s.stream(ctx, out, &last)
		if exited {
			return
		}
		if err != nil && api.IsCode(err, api.CodeNotFound) {
			s.code, s.err = -1, err
			return
		}
		if got {
			failing = 0
		}
		failing++
		if ctx.Err() != nil || failing > 5 {
			s.code = -1
			if err == nil {
				err = fmt.Errorf("jailops: the console stream ended without an exit status")
			}
			s.err = err
			return
		}
		select {
		case <-time.After(time.Duration(failing) * 200 * time.Millisecond):
		case <-ctx.Done():
		}
	}
}

// stream runs one connection to the output stream.
func (s *ConsoleSession) stream(ctx context.Context, out *io.PipeWriter, last *string) (got, exited bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.j.c.buildURL(s.base+"/output", nil), nil)
	if err != nil {
		return false, false, err
	}
	s.j.c.decorate(req, newRequestID())
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	if *last != "" {
		req.Header.Set("Last-Event-ID", *last)
	}
	resp, err := s.j.c.t.sc.Do(req)
	if err != nil {
		return false, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_, derr := decodeResponse(resp.StatusCode, data)
		if derr == nil {
			derr = errorFromStatus(resp.StatusCode, data)
		}
		return false, false, derr
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), maxEventLine)
	var id string
	var data []string
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if len(data) > 0 {
				done, err := s.deliver(out, strings.Join(data, "\n"))
				if err != nil {
					s.code, s.err = -1, err
					return got, true, err
				}
				got = true
				if id != "" {
					*last = id
				}
				if done {
					return got, true, nil
				}
			}
			data = nil
			continue
		}
		if line[0] == ':' {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "id":
			id = value
		case "data":
			data = append(data, value)
		}
	}
	return got, false, sc.Err()
}

// deliver handles one event; it reports whether it was the exit status.
func (s *ConsoleSession) deliver(out *io.PipeWriter, payload string) (bool, error) {
	var ev api.Event
	if err := json.Unmarshal([]byte(payload), &ev); err != nil {
		return false, fmt.Errorf("jailops: bad console event: %w", err)
	}
	var m struct {
		Data string `json:"data"`
		Exit *int   `json:"exit"`
	}
	if err := json.Unmarshal(ev.Metadata, &m); err != nil {
		return false, fmt.Errorf("jailops: bad console event: %w", err)
	}
	if m.Exit != nil {
		s.code = *m.Exit
		return true, nil
	}
	b, err := base64.StdEncoding.DecodeString(m.Data)
	if err != nil {
		return false, fmt.Errorf("jailops: bad console output: %w", err)
	}
	if _, err := out.Write(b); err != nil {
		return false, io.ErrClosedPipe
	}
	return false, nil
}
