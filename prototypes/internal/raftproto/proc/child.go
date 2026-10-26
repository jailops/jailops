package proc

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"jailops.net/jailops/prototypes/internal/raftproto/api"
)

// ChildArg is the hidden first argument that turns the binary into a node process.
const ChildArg = "__node"

type req struct {
	Opts    api.StartOpts `json:"opts"`
	Cmd     []byte        `json:"cmd"`
	Key     string        `json:"key"`
	ID      string        `json:"id"`
	Addr    string        `json:"addr"`
	Timeout time.Duration `json:"timeout"`
}

type resp struct {
	Err    string      `json:"err,omitempty"`
	Status *api.Status `json:"status,omitempty"`
	Val    string      `json:"val,omitempty"`
	Found  bool        `json:"found,omitempty"`
}

// ChildMain serves node RPCs until stdin closes (the parent died) or it is killed.
func ChildMain(args []string) int {
	fs := flag.NewFlagSet(ChildArg, flag.ContinueOnError)
	specJSON := fs.String("spec", "", "node spec json")
	ctl := fs.String("ctl", "", "control listen address")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	var spec api.NodeSpec
	if err := json.Unmarshal([]byte(*specJSON), &spec); err != nil {
		fmt.Fprintln(os.Stderr, "bad spec:", err)
		return 2
	}
	n, err := NewLocal(spec)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	go func() { io.Copy(io.Discard, os.Stdin); os.Exit(0) }()
	mux := http.NewServeMux()
	handle := func(name string, f func(r req) resp) {
		mux.HandleFunc("/rpc/"+name, func(w http.ResponseWriter, hr *http.Request) {
			var r req
			json.NewDecoder(hr.Body).Decode(&r)
			json.NewEncoder(w).Encode(f(r))
		})
	}
	errResp := func(err error) resp {
		if err != nil {
			return resp{Err: err.Error()}
		}
		return resp{}
	}
	handle("ping", func(req) resp { return resp{} })
	handle("start", func(r req) resp { return errResp(n.Start(r.Opts)) })
	handle("stop", func(req) resp { return errResp(n.Stop()) })
	handle("status", func(req) resp {
		s, err := n.Status()
		if err != nil {
			return errResp(err)
		}
		return resp{Status: &s}
	})
	handle("propose", func(r req) resp { return errResp(n.Propose(r.Cmd, r.Timeout)) })
	handle("get", func(r req) resp {
		v, ok, err := n.Get(r.Key)
		if err != nil {
			return errResp(err)
		}
		return resp{Val: v, Found: ok}
	})
	handle("snapshot", func(req) resp { return errResp(n.Snapshot()) })
	handle("addvoter", func(r req) resp { return errResp(n.AddVoter(r.ID, r.Addr, r.Timeout)) })
	handle("removevoter", func(r req) resp { return errResp(n.RemoveVoter(r.ID, r.Timeout)) })
	if err := http.ListenAndServe(*ctl, mux); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
