package priv

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"time"

	"jailops.net/jailops/sdk/api"
)

// Exec implements Host: it starts the command in the helper and returns when
// the helper has started it (so a refusal is an error here). Cancelling ctx
// ends the session and the helper kills its process group; so does losing
// the connection.
func (c *Client) Exec(ctx context.Context, jail string, argv []string, o ExecOpts) (ExecSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if o.TTY && (o.Rows != 0 || o.Cols != 0) {
		if err := checkWinsize(o.Rows, o.Cols); err != nil {
			return nil, err
		}
	}
	cc, err := c.connection()
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(sessArgs{Jail: jail, Argv: argv, TTY: o.TTY, Rows: o.Rows, Cols: o.Cols, Env: o.Env, User: o.User})
	if err != nil {
		return nil, err
	}
	call := &clientCall{res: make(chan reply, 1), window: make(chan struct{}, Window),
		out: make(chan []byte, Window), ctl: make(chan *Frame, 4)}
	for i := 0; i < Window; i++ {
		call.window <- struct{}{}
	}
	if !o.TTY {
		call.errOut = make(chan []byte, Window)
	}
	cc.mu.Lock()
	cc.next++
	call.id = cc.next
	cc.call[call.id] = call
	cc.mu.Unlock()
	forget := func() { cc.mu.Lock(); delete(cc.call, call.id); cc.mu.Unlock() }

	req := &Frame{Kind: KindReq, ID: call.id, Op: OpExecSession, Args: raw}
	if a := AdminFrom(ctx); a != nil {
		req.Admin = &Admin{User: a.User}
	}
	if err := cc.fc.write(req); err != nil {
		forget()
		cc.fail(err)
		return nil, api.Errorf(api.CodeInternal, "send to the privileged helper: %v", err)
	}
	// Wait for "started" or the refusal. A command that exits at once can make both the "started" frame and the
	// final reply ready together; the session then exists and is finished with that reply.
	var early *reply
	select {
	case m := <-call.ctl:
		if m.Op != "started" {
			forget()
			return nil, api.Errorf(api.CodeInternal, "unexpected control frame %q", m.Op)
		}
	case r := <-call.res:
		if r.f.Err == nil {
			select {
			case m := <-call.ctl:
				if m.Op == "started" {
					early = &r
				}
			default:
			}
		}
		if early == nil {
			forget()
			closeFiles(r.files)
			if r.f.Err != nil {
				return nil, fromWire(r.f.Err)
			}
			return nil, api.Errorf(api.CodeInternal, "the helper ended the session before it started")
		}
	case <-ctx.Done():
		_ = cc.fc.write(&Frame{Kind: KindCancel, ID: call.id})
		forget()
		return nil, ctx.Err()
	case <-cc.dead:
		forget()
		cc.mu.Lock()
		err := cc.err
		cc.mu.Unlock()
		return nil, err
	}

	s := &clientSession{cc: cc, call: call, tty: o.TTY, done: make(chan struct{}), forget: forget}
	s.stdin = &sessStdin{s: s}
	s.stdout = s.pipe(call.out)
	if call.errOut != nil {
		s.stderr = s.pipe(call.errOut)
	}
	if early != nil {
		s.finish(*early)
		return s, nil
	}
	go func() {
		select {
		case r := <-call.res:
			s.finish(r)
		case <-ctx.Done():
			_ = cc.fc.write(&Frame{Kind: KindCancel, ID: call.id})
			select {
			case r := <-call.res:
				s.finish(r)
			case <-time.After(5 * time.Second):
				s.finish(reply{f: &Frame{Err: toWire(ctx.Err())}})
			}
		case <-cc.dead:
			cc.mu.Lock()
			err := cc.err
			cc.mu.Unlock()
			s.finish(reply{f: &Frame{Err: toWire(err)}})
		}
	}()
	return s, nil
}

type clientSession struct {
	cc     *clientConn
	call   *clientCall
	tty    bool
	forget func()

	stdin  *sessStdin
	stdout io.Reader
	stderr io.Reader

	done chan struct{}
	exit int
	err  error
	once sync.Once
}

func (s *clientSession) finish(r reply) {
	s.once.Do(func() {
		closeFiles(r.files)
		if r.f.Err != nil {
			s.err = fromWire(r.f.Err)
		} else {
			var res sessRes
			_ = json.Unmarshal(r.f.Result, &res)
			s.exit = res.Exit
		}
		s.forget()
		close(s.done)
	})
}

// pipe delivers output chunks through an io.Pipe, acknowledging each chunk
// after the reader took it so a slow reader slows the helper.
func (s *clientSession) pipe(ch chan []byte) io.Reader {
	pr, pw := io.Pipe()
	go func() {
		for {
			select {
			case b, ok := <-ch:
				if !ok {
					pw.Close()
					return
				}
				if _, err := pw.Write(b); err != nil {
					// The reader went away: keep acknowledging so the
					// helper is not stalled.
					for range ch {
						_ = s.cc.fc.write(&Frame{Kind: KindAck, ID: s.call.id})
					}
					return
				}
				_ = s.cc.fc.write(&Frame{Kind: KindAck, ID: s.call.id})
			case <-s.cc.dead:
				pw.CloseWithError(io.ErrUnexpectedEOF)
				return
			}
		}
	}()
	return pr
}

func (s *clientSession) Stdin() io.WriteCloser { return s.stdin }
func (s *clientSession) Stdout() io.Reader     { return s.stdout }
func (s *clientSession) Stderr() io.Reader     { return s.stderr }

func (s *clientSession) ctl(op string, m sessCtl) error {
	select {
	case <-s.done:
		return api.Errorf(api.CodeNotFound, "the session has ended")
	default:
	}
	b, _ := json.Marshal(m)
	if err := s.cc.fc.write(&Frame{Kind: KindCtl, ID: s.call.id, Op: op, Args: b}); err != nil {
		return api.Errorf(api.CodeInternal, "send to the privileged helper: %v", err)
	}
	return nil
}

func (s *clientSession) Resize(rows, cols uint16) error {
	if !s.tty {
		return invalid("the session has no terminal")
	}
	if err := checkWinsize(rows, cols); err != nil {
		return err
	}
	return s.ctl("resize", sessCtl{Rows: rows, Cols: cols})
}

func (s *clientSession) Signal(sig int) error {
	if err := checkSignal(sig); err != nil {
		return err
	}
	return s.ctl("signal", sessCtl{Sig: sig})
}

func (s *clientSession) Wait() (int, error) {
	<-s.done
	return s.exit, s.err
}

// sessStdin sends the request stream, within the window.
type sessStdin struct {
	s      *clientSession
	mu     sync.Mutex
	closed bool
}

func (w *sessStdin) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, io.ErrClosedPipe
	}
	total := 0
	for len(p) > 0 {
		n := min(len(p), ChunkSize)
		select {
		case <-w.s.call.window:
		case <-w.s.done:
			return total, io.ErrClosedPipe
		case <-w.s.cc.dead:
			return total, io.ErrClosedPipe
		}
		if err := w.s.cc.fc.write(&Frame{Kind: KindData, ID: w.s.call.id, Data: append([]byte(nil), p[:n]...)}); err != nil {
			return total, err
		}
		total += n
		p = p[n:]
	}
	return total, nil
}

func (w *sessStdin) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	select {
	case <-w.s.done:
		return nil
	default:
	}
	return w.s.cc.fc.write(&Frame{Kind: KindEnd, ID: w.s.call.id})
}
