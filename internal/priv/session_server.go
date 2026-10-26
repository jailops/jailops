package priv

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"time"
)

// serveSession runs one jail.session request: it starts the command with
// Host.Exec and pumps the request stream into its stdin, its stdout and
// stderr into data frames (stderr on channel "e") and the client's control
// frames into Resize and Signal. The reply carries the exit code. When the
// request ends for any reason (cancel, deadline, disconnect) the session's
// process group is killed.
func serveSession(ctx context.Context, h Host, a sessArgs, c *callIO) (any, error) {
	s, err := h.Exec(ctx, a.Jail, a.Argv, ExecOpts{TTY: a.TTY, Rows: a.Rows, Cols: a.Cols, Env: a.Env, User: a.User})
	if err != nil {
		return nil, err
	}
	if cl, ok := s.(sessionCloser); ok {
		defer cl.Close()
	}
	if err := c.Send(&Frame{Kind: KindCtl, Op: "started"}); err != nil {
		return nil, err
	}
	var pumps sync.WaitGroup
	pump := func(r io.Reader, w io.Writer) {
		defer pumps.Done()
		_, _ = io.Copy(w, r)
	}
	pumps.Add(1)
	go pump(s.Stdout(), c.Out)
	if e := s.Stderr(); e != nil {
		pumps.Add(1)
		go pump(e, c.ErrOut)
	}
	go func() { // stdin: the request stream; its end closes the command's stdin
		_, _ = io.Copy(s.Stdin(), c.In)
		_ = s.Stdin().Close()
	}()
	go func() {
		for {
			select {
			case f := <-c.Ctl:
				var m sessCtl
				if json.Unmarshal(f.Args, &m) != nil {
					continue
				}
				// Errors cannot be reported on a control frame; the client
				// checks the same rules before sending.
				switch f.Op {
				case "resize":
					_ = s.Resize(m.Rows, m.Cols)
				case "signal":
					_ = s.Signal(m.Sig)
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	code, werr := s.Wait()
	done := make(chan struct{})
	go func() { pumps.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second): // a leftover holder of the terminal
		if cl, ok := s.(sessionCloser); ok {
			_ = cl.Close()
		}
		<-done
	}
	if werr != nil {
		return nil, werr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return sessRes{Exit: code}, nil
}
