package proc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"

	"jailops.net/jailops/prototypes/internal/raftproto/api"
	"jailops.net/jailops/prototypes/internal/raftproto/proxy"
)

// Remote is an api.Node living in a child process.
type Remote struct {
	spec api.NodeSpec
	exe  string
	ctl  string
	mu   sync.Mutex
	cmd  *exec.Cmd
	in   io.WriteCloser
	cli  *http.Client
}

// NewRemote returns a node whose process is spawned by Start. exe is the binary to re-execute
// (os.Executable() in production; the test binary in tests, which handles ChildArg in TestMain).
func NewRemote(spec api.NodeSpec, exe string) *Remote {
	return &Remote{spec: spec, exe: exe, cli: &http.Client{Timeout: 60 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}}
}

func (r *Remote) ID() string { return r.spec.ID }

func (r *Remote) spawn() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cmd != nil {
		return nil
	}
	ctl, err := proxy.FreeAddr()
	if err != nil {
		return err
	}
	sj, _ := json.Marshal(r.spec)
	cmd := exec.Command(r.exe, ChildArg, "--spec", string(sj), "--ctl", ctl)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	r.cmd, r.in, r.ctl = cmd, in, ctl
	for i := 0; i < 100; i++ {
		if _, err := r.call("ping", req{}); err == nil {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return errors.New("child did not come up")
}

func (r *Remote) call(method string, q req) (resp, error) {
	b, _ := json.Marshal(q)
	hr, err := r.cli.Post("http://"+r.ctl+"/rpc/"+method, "application/json", bytes.NewReader(b))
	if err != nil {
		return resp{}, err
	}
	defer hr.Body.Close()
	var out resp
	if err := json.NewDecoder(hr.Body).Decode(&out); err != nil {
		return resp{}, err
	}
	if out.Err != "" {
		return out, errors.New(out.Err)
	}
	return out, nil
}

func (r *Remote) running() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cmd != nil
}

func (r *Remote) Start(o api.StartOpts) error {
	if err := r.spawn(); err != nil {
		return err
	}
	_, err := r.call("start", req{Opts: o})
	return err
}

func (r *Remote) kill() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cmd == nil {
		return nil
	}
	err := r.cmd.Process.Kill() // SIGKILL
	r.cmd.Wait()
	r.cmd, r.in = nil, nil
	return err
}

// Crash is SIGKILL of the whole node process.
func (r *Remote) Crash() error { return r.kill() }

func (r *Remote) Stop() error {
	if !r.running() {
		return nil
	}
	_, err := r.call("stop", req{})
	r.kill()
	return err
}

func (r *Remote) rpc(method string, q req) (resp, error) {
	if !r.running() {
		return resp{}, errors.New("node not running")
	}
	return r.call(method, q)
}

func (r *Remote) Status() (api.Status, error) {
	o, err := r.rpc("status", req{})
	if err != nil {
		return api.Status{}, err
	}
	if o.Status == nil {
		return api.Status{}, fmt.Errorf("empty status")
	}
	return *o.Status, nil
}

func (r *Remote) Propose(cmd []byte, t time.Duration) error {
	_, err := r.rpc("propose", req{Cmd: cmd, Timeout: t})
	return err
}

func (r *Remote) Get(k string) (string, bool, error) {
	o, err := r.rpc("get", req{Key: k})
	return o.Val, o.Found, err
}

func (r *Remote) Snapshot() error { _, err := r.rpc("snapshot", req{}); return err }

func (r *Remote) AddVoter(id, addr string, t time.Duration) error {
	_, err := r.rpc("addvoter", req{ID: id, Addr: addr, Timeout: t})
	return err
}

func (r *Remote) RemoveVoter(id string, t time.Duration) error {
	_, err := r.rpc("removevoter", req{ID: id, Timeout: t})
	return err
}

var _ api.Node = (*Remote)(nil)
