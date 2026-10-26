//go:build tools

package prototypes

import (
	_ "github.com/hashicorp/raft"
	_ "github.com/hashicorp/raft-boltdb/v2"
	_ "go.etcd.io/raft/v3"
	_ "golang.org/x/net/ipv6"
	_ "golang.org/x/sys/unix"
)
