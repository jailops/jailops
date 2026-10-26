package hostbackup

import (
	"context"
	"crypto/tls"
	"fmt"
	"strings"
	"sync"
	"time"

	"jailops.net/jailops/internal/node/client"
	"jailops.net/jailops/internal/node/client/enginerepo"
	"jailops.net/jailops/internal/node/wire"
	"jailops.net/jailops/internal/repo/datastore"
	"jailops.net/jailops/internal/repo/engine"
	"jailops.net/jailops/internal/repo/keys"
	"jailops.net/jailops/internal/repo/store"
	"jailops.net/jailops/sdk/api"
)

// Remote is a repository the host backs up to: a backup node over HTTPS, or a
// directory. It is the read side, the write side and the escrow of one
// repository.
type Remote interface {
	engine.Repository
	keys.EscrowChecker
	keys.EscrowStore
	// Refresh reads the repository's identity (its key domain), so that a
	// host with another key is refused before any data moves.
	Refresh(ctx context.Context) error
	NewUploader(ctx context.Context, group, snapshot, previous string) (engine.Uploader, error)
	GetEscrow(ctx context.Context, domain string) ([]byte, error)
	// ListEscrow lists the key domains that have an escrow blob.
	ListEscrow(ctx context.Context) ([]string, error)
	// Groups lists the group ids of the repository.
	Groups(ctx context.Context) ([]string, error)
	// RecordRestoreTest reports a restore test to the repository's owner.
	RecordRestoreTest(ctx context.Context, rt wire.RestoreTest) error
	Close()
}

// nodeRemote is a Remote over the HTTP client of a backup node.
type nodeRemote struct {
	*enginerepo.Repo
	c *client.Client
	// domain is guarded by mu.
	mu     sync.Mutex
	domain string
}

// NewNodeRemote wraps a client of a backup node.
func NewNodeRemote(c *client.Client) Remote {
	r := &nodeRemote{c: c}
	r.Repo = &enginerepo.Repo{C: c}
	return r
}

func (r *nodeRemote) Refresh(ctx context.Context) error {
	info, err := r.c.Info(ctx)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.domain = info.KeyDomain
	r.mu.Unlock()
	return nil
}

func (r *nodeRemote) KeyDomain() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.domain
}

func (r *nodeRemote) NewUploader(ctx context.Context, group, snapshot, previous string) (engine.Uploader, error) {
	return enginerepo.NewUploader(r.c)(ctx, group, snapshot, previous)
}

func (r *nodeRemote) GetEscrow(ctx context.Context, domain string) ([]byte, error) {
	return r.c.GetEscrow(ctx, domain)
}

func (r *nodeRemote) ListEscrow(ctx context.Context) ([]string, error) { return r.c.ListEscrow(ctx) }

func (r *nodeRemote) Groups(ctx context.Context) ([]string, error) {
	gs, err := r.c.Groups(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(gs))
	for i, g := range gs {
		out[i] = g.GroupID
	}
	return out, nil
}

func (r *nodeRemote) RecordRestoreTest(ctx context.Context, rt wire.RestoreTest) error {
	return r.c.RecordRestoreTest(ctx, rt)
}

func (r *nodeRemote) Close() { r.c.Close() }

// dirRemote is a Remote over a directory repository (an attached disk).
type dirRemote struct {
	engine.StoreRepo
	s *store.Store
}

// NewDirRemote opens (or fails on) the repository stored in a directory.
func NewDirRemote(ctx context.Context, path string) (Remote, error) {
	b, err := datastore.NewDirBackend(path)
	if err != nil {
		return nil, err
	}
	s, err := store.Open(ctx, b)
	if err != nil {
		return nil, fmt.Errorf("opening the repository in %s: %w", path, err)
	}
	return NewStoreRemote(s), nil
}

// NewStoreRemote wraps an open store (tests, local repositories).
func NewStoreRemote(s *store.Store) Remote {
	return &dirRemote{StoreRepo: engine.StoreRepo{S: s}, s: s}
}

func (r *dirRemote) Refresh(context.Context) error { return nil }
func (r *dirRemote) HasEscrow(ctx context.Context, d string) (bool, error) {
	return r.s.HasEscrow(ctx, d)
}
func (r *dirRemote) PutEscrow(ctx context.Context, d string, b []byte) error {
	return r.s.PutEscrow(ctx, d, b)
}
func (r *dirRemote) GetEscrow(ctx context.Context, d string) ([]byte, error) {
	return r.s.GetEscrow(ctx, d)
}
func (r *dirRemote) ListEscrow(ctx context.Context) ([]string, error) { return r.s.ListEscrow(ctx) }
func (r *dirRemote) Groups(ctx context.Context) ([]string, error)     { return r.s.ListGroups(ctx) }
func (r *dirRemote) NewUploader(ctx context.Context, group, snapshot, previous string) (engine.Uploader, error) {
	return engine.StoreUploader(r.s)(ctx, group, snapshot, previous)
}
func (r *dirRemote) RecordRestoreTest(ctx context.Context, rt wire.RestoreTest) error {
	return r.s.SetVerify(ctx, rt.Group, rt.Snapshot, store.VerifyRecord{Failed: !rt.OK, Verified: time.Now().UTC(), Mode: rt.Method, Detail: strings.Join(rt.Problems, "; ")})
}
func (r *dirRemote) Close() {}

// remoteFor resolves the repository of a target name.
func (s *Service) remoteFor(ctx context.Context, target string) (Remote, error) {
	if s.deps.Remote != nil {
		return s.deps.Remote, nil
	}
	key := s.currentKey()
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.remotes[target]; ok {
		return r, nil
	}
	var r Remote
	node := s.cfg.Node
	var spec *api.BackupTargetSpec
	if s.deps.Policies != nil && target != "" {
		ts, err := s.deps.Policies.BackupTargets(ctx)
		if err != nil {
			return nil, err
		}
		for _, t := range ts {
			if t.Metadata.Name == target {
				sp := t.Spec
				spec = &sp
			}
		}
	}
	switch {
	case spec != nil && spec.Kind == "directory":
		d, err := NewDirRemote(ctx, spec.Path)
		if err != nil {
			return nil, err
		}
		r = d
	case spec != nil && spec.Address != "":
		c, err := s.newClient(NodeConfig{Address: spec.Address, Fingerprint: spec.Fingerprint, CertFile: node.CertFile, KeyFile: node.KeyFile, TLS: node.TLS}, key)
		if err != nil {
			return nil, err
		}
		r = NewNodeRemote(c)
	case node.Address != "" && (target == "" || node.Name == "" || node.Name == target):
		c, err := s.newClient(node, key)
		if err != nil {
			return nil, err
		}
		r = NewNodeRemote(c)
	default:
		return nil, api.Errorf(api.CodeNotFound, "no backup node or directory is configured for target %q", target)
	}
	if s.remotes == nil {
		s.remotes = map[string]Remote{}
	}
	s.remotes[target] = r
	return r, nil
}

// newClient builds the HTTP client of a node. The key domain is not sent here:
// a node whose recorded domain differs from the key is refused by the engine
// (Remote.KeyDomain) before a session opens.
func (s *Service) newClient(n NodeConfig, key *keys.Key) (*client.Client, error) {
	cfg := client.Config{Address: nodeAddress(n.Address), Fingerprint: n.Fingerprint, TLS: n.TLS}
	if n.TLS == nil {
		if n.CertFile == "" || n.KeyFile == "" {
			return nil, api.Errorf(api.CodeInvalid, "the host needs a client certificate (cert_file and key_file) to talk to node %s", n.Address)
		}
		cert, err := tls.LoadX509KeyPair(n.CertFile, n.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("loading the host certificate: %w", err)
		}
		cfg.Cert = cert
	}
	if key != nil {
		cfg.KeyDomain = key.DomainID()
	}
	return client.New(cfg)
}

// dropRemotes closes cached clients (the key changed, so their key domain did).
func (s *Service) dropRemotes() {
	s.mu.Lock()
	rs := s.remotes
	s.remotes = nil
	s.mu.Unlock()
	for _, r := range rs {
		r.Close()
	}
}

// nodeAddress turns what a target says ("https://host:port", as the CLI documents it, or host:port as the
// host configuration has it) into the host:port the client dials.
func nodeAddress(a string) string {
	a = strings.TrimPrefix(strings.TrimSpace(a), "https://")
	return strings.TrimSuffix(a, "/")
}
