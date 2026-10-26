// Package proc runs a node in a child process controlled over a localhost HTTP socket, so the
// crash scenario can use real SIGKILL. The same binary is the child (hidden subcommand).
package proc

import (
	"fmt"

	"jailops.net/jailops/prototypes/internal/raftproto/api"
	"jailops.net/jailops/prototypes/internal/raftproto/etcdraft"
	"jailops.net/jailops/prototypes/internal/raftproto/hashi"
)

// NewLocal builds an in-process node of the library named in spec.
func NewLocal(spec api.NodeSpec) (api.Node, error) {
	switch spec.Lib {
	case "hashicorp":
		return hashi.New(spec), nil
	case "etcd":
		return etcdraft.New(spec), nil
	}
	return nil, fmt.Errorf("unknown library %q", spec.Lib)
}
