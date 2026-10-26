package fake

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"jailops.net/jailops/sdk/api"
)

// ExecCall is one command the fake ran; the fake records every exec.
type ExecCall struct {
	Jail string
	Argv []string
	TTY  bool
}

// ExecCalls returns the commands run so far.
func (s *Server) ExecCalls() []ExecCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ExecCall(nil), s.execCalls...)
}

// jailExec implements POST /1.0/jails/<n>/exec with a few built-in programs:
// "echo ARGS", "cat" (stdin to stdout), "stderr TEXT", "exit N".
func (s *Server) jailExec(w http.ResponseWriter, r *http.Request) {
	req, e := decode[api.ExecRequest](r)
	if e != nil {
		writeErr(w, e)
		return
	}
	name := r.PathValue("name")
	s.mu.Lock()
	_, j := s.jails.find(r, name)
	var perr *api.Error
	switch {
	case j == nil:
		perr = notFound("jail", name)
	case j.Status.Power != api.PowerRunning:
		perr = api.Errorf(api.CodeBusy, "jail %q is not running", name)
	}
	if perr == nil {
		s.execCalls = append(s.execCalls, ExecCall{Jail: name, Argv: req.Argv, TTY: req.TTY})
	}
	s.mu.Unlock()
	if perr != nil {
		writeErr(w, perr)
		return
	}
	if !strings.EqualFold(r.Header.Get("Upgrade"), api.ExecProtocol) {
		writeErr(w, api.Errorf(api.CodeInvalid, "exec needs Upgrade: %s", api.ExecProtocol))
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		writeErr(w, api.Errorf(api.CodeInternal, "the connection cannot be upgraded"))
		return
	}
	conn, brw, err := hj.Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	fmt.Fprintf(brw, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: %s\r\n\r\n", api.ExecProtocol)
	brw.Flush()
	argv := req.Argv
	if req.Shell || len(argv) == 0 {
		argv = []string{"cat"}
	}
	code := runFakeProgram(brw.Reader, conn, argv)
	_ = api.WriteFrame(conn, api.FrameExit, api.Int32Payload(int32(code)))
}

func runFakeProgram(in *bufio.Reader, out io.Writer, argv []string) int {
	switch argv[0] {
	case "echo":
		_ = api.WriteFrame(out, api.FrameStdout, []byte(strings.Join(argv[1:], " ")+"\n"))
	case "stderr":
		_ = api.WriteFrame(out, api.FrameStderr, []byte(strings.Join(argv[1:], " ")))
	case "exit":
		if len(argv) > 1 {
			n, _ := strconv.Atoi(argv[1])
			return n
		}
	case "cat":
		for {
			t, p, err := api.ReadFrame(in)
			if err != nil {
				return 1
			}
			if t == api.FrameStdin {
				if len(p) == 0 {
					return 0
				}
				_ = api.WriteFrame(out, api.FrameStdout, p)
			}
		}
	default:
		return 127
	}
	return 0
}
