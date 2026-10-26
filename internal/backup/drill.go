package backup

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"

	"jailops.net/jailops/sdk/api"
)

// Depth says how far a restore test goes (design section 9.7).
type Depth string

const (
	// DepthDeep reads and decrypts every chunk of the snapshot and recomputes
	// each chunk id. It restores nothing.
	DepthDeep Depth = "deep"
	// DepthExtract restores the snapshot into a scratch directory and checks
	// every file hash.
	DepthExtract Depth = "extract"
	// DepthFull restores into a scratch dataset, attaches the jail to an
	// isolated network, starts it and runs its health check.
	DepthFull Depth = "full"
)

// ParseDepth parses BackupPolicySpec.VerifyMode. An empty mode is DepthDeep,
// the cheapest test that still proves the data can be decrypted.
func ParseDepth(mode string) (Depth, error) {
	switch d := Depth(mode); d {
	case "":
		return DepthDeep, nil
	case DepthDeep, DepthExtract, DepthFull:
		return d, nil
	}
	return "", invalid("unknown verify mode %q (want deep, extract or full)", mode)
}

// EnvKind says what kind of machine a restore test would run on.
type EnvKind string

const (
	// EnvHost is a jail host holding the key.
	EnvHost EnvKind = "host"
	// EnvRecovery is a dedicated recovery host that loads the recovery key
	// temporarily.
	EnvRecovery EnvKind = "recovery"
	// EnvBackupNode is the backup node. It holds ciphertext and no key, so it
	// can never run a restore test.
	EnvBackupNode EnvKind = "backup-node"
)

// DrillPlan is a planned restore test of one backup. Planning is pure; the
// backup node may schedule tests but never executes them.
type DrillPlan struct {
	ID    string
	Depth Depth
	// Backup is the repository snapshot id; Jail the jail it holds.
	Backup string
	Jail   string
	// KeyDomain is the master key domain the executor must hold.
	KeyDomain string
	// Scratch is the directory (extract) or dataset (full) the snapshot is
	// restored into. It is empty for deep tests.
	Scratch string
	// Network is the isolated network name and NetSpec its definition
	// (private range, no uplink, no NAT, no DNS). Both are empty unless the
	// depth is full.
	Network string
	NetSpec api.NetworkSpec
	// Healthcheck is the jail's health check command (full tests).
	Healthcheck string
}

// PlanDrill plans a restore test of one backup. scratchRoot is the directory
// or dataset under which scratch space is created. A backup that failed
// verification is refused: its data is known to be incomplete.
func PlanDrill(b api.Backup, depth Depth, scratchRoot, id string) (DrillPlan, error) {
	if !validID(id) {
		return DrillPlan{}, invalid("invalid drill id %q", id)
	}
	if depth != DepthDeep && depth != DepthExtract && depth != DepthFull {
		return DrillPlan{}, invalid("unknown drill depth %q", depth)
	}
	switch {
	case b.Spec.Snapshot == "":
		return DrillPlan{}, invalid("backup names no snapshot")
	case b.Spec.KeyDomain == "":
		return DrillPlan{}, invalid("backup records no key domain; cannot tell which key decrypts it")
	case b.Status.Failed:
		return DrillPlan{}, invalid("backup %s failed verification; it cannot be tested", b.Spec.Snapshot)
	case depth != DepthDeep && scratchRoot == "":
		return DrillPlan{}, invalid("a %s test needs a scratch location", depth)
	}
	p := DrillPlan{ID: id, Depth: depth, Backup: b.Spec.Snapshot, Jail: b.Spec.Jail, KeyDomain: b.Spec.KeyDomain}
	if depth == DepthDeep {
		return p, nil
	}
	p.Scratch = scratchRoot + "/drill-" + id
	if depth == DepthExtract {
		return p, nil
	}
	p.Healthcheck = b.Spec.Bundle.Spec.Update.Healthcheck
	p.Network = "jodrill-" + id
	h := sha256.Sum256([]byte("jailops/drill-net\x00" + id))
	n := int(binary.BigEndian.Uint16(h[:2])) % 512 // 198.18.0.0/15 has 512 /24s
	p.NetSpec = api.NetworkSpec{IPv4: &api.IPv4Net{
		Subnet:  fmt.Sprintf("198.%d.%d.0/24", 18+n/256, n%256),
		Gateway: fmt.Sprintf("198.%d.%d.1", 18+n/256, n%256),
		Range:   fmt.Sprintf("198.%d.%d.10-198.%d.%d.250", 18+n/256, n%256, 18+n/256, n%256),
	}}
	return p, nil
}

// Executor is a machine that may run restore tests.
type Executor struct {
	Name string
	Kind EnvKind
	// Keys are the key domains the machine holds.
	Keys map[string]bool
}

// SelectExecutor picks the first executor that may run the plan: never a
// backup node, and holding the key domain of the backup.
func SelectExecutor(p DrillPlan, candidates []Executor) (Executor, error) {
	for _, c := range candidates {
		if c.Kind == EnvBackupNode || !c.Keys[p.KeyDomain] {
			continue
		}
		return c, nil
	}
	return Executor{}, api.Errorf(api.CodeNotFound, "no host holds key domain %q; restore tests cannot run on the backup node", p.KeyDomain).
		WithHint("run the test on the owning host or on a recovery host that loads the recovery key")
}

// Verifier reads a backup through the repository engine. The engine
// implements it; this package does not import the engine.
type Verifier interface {
	// VerifyChunks reads every chunk of the snapshot, decrypts it and
	// recomputes its id.
	VerifyChunks(ctx context.Context, p DrillPlan) error
	// ExtractScratch restores the snapshot's files into the directory
	// p.Scratch and checks each file against its recorded hash.
	ExtractScratch(ctx context.Context, p DrillPlan) error
	// RemoveScratchDir removes p.Scratch after ExtractScratch, also after a
	// partial extraction.
	RemoveScratchDir(ctx context.Context, p DrillPlan) error
	// RestoreScratch restores the snapshot into the dataset p.Scratch.
	RestoreScratch(ctx context.Context, p DrillPlan) error
	// DestroyScratch destroys the dataset p.Scratch, also after a partial restore.
	DestroyScratch(ctx context.Context, p DrillPlan) error
}

// JailRunner runs the restored jail for a full test.
type JailRunner interface {
	CreateNetwork(ctx context.Context, name string, spec api.NetworkSpec) error
	DestroyNetwork(ctx context.Context, name string) error
	// StartJail starts the jail from p.Scratch attached to p.Network.
	StartJail(ctx context.Context, p DrillPlan) error
	StopJail(ctx context.Context, p DrillPlan) error
}

// DrillEnv is the machine a restore test runs on.
type DrillEnv interface {
	Verifier
	JailRunner
	Kind() EnvKind
	// HoldsKey reports whether the machine can decrypt the key domain.
	HoldsKey(domain string) bool
}

// HealthCheck runs the jail's health check inside a full test.
type HealthCheck func(ctx context.Context, p DrillPlan) error

// DrillResult is the outcome of a restore test.
type DrillResult struct {
	Depth Depth
	// OK is true when every step of the depth passed.
	OK bool
	// CleanupErr is non-nil when cleanup left something behind.
	CleanupErr error
}

// RunDrill runs the test at the plan's depth and always cleans up through a
// reverter, also when a step fails or ctx is cancelled. It refuses to run on
// a backup node and on a machine without the key domain. A full test needs a
// health check.
func RunDrill(ctx context.Context, p DrillPlan, env DrillEnv, check HealthCheck) (res DrillResult, err error) {
	res.Depth = p.Depth
	if env.Kind() == EnvBackupNode {
		return res, api.Errorf(api.CodeForbidden, "restore tests need the key and never run on the backup node")
	}
	if !env.HoldsKey(p.KeyDomain) {
		return res, api.Errorf(api.CodeForbidden, "this machine does not hold key domain %q", p.KeyDomain)
	}
	if p.Depth == DepthFull && check == nil {
		return res, invalid("a full test needs a health check")
	}
	var rev reverter
	defer func() {
		// Cleanup must run even when ctx is cancelled.
		res.CleanupErr = rev.revert()
		if res.CleanupErr != nil && err == nil {
			err = fmt.Errorf("drill cleanup: %w", res.CleanupErr)
		}
	}()
	cctx := func() context.Context { return context.WithoutCancel(ctx) }
	switch p.Depth {
	case DepthDeep:
		if err = env.VerifyChunks(ctx, p); err != nil {
			return res, err
		}
	case DepthExtract:
		// Registered before the call: a partial extraction is removed too.
		rev.add(func() error { return env.RemoveScratchDir(cctx(), p) })
		if err = env.ExtractScratch(ctx, p); err != nil {
			return res, err
		}
	case DepthFull:
		rev.add(func() error { return env.DestroyNetwork(cctx(), p.Network) })
		if err = env.CreateNetwork(ctx, p.Network, p.NetSpec); err != nil {
			return res, err
		}
		rev.add(func() error { return env.DestroyScratch(cctx(), p) })
		if err = env.RestoreScratch(ctx, p); err != nil {
			return res, err
		}
		rev.add(func() error { return env.StopJail(cctx(), p) })
		if err = env.StartJail(ctx, p); err != nil {
			return res, err
		}
		if herr := check(ctx, p); herr != nil {
			return res, fmt.Errorf("health check failed: %w", herr)
		}
	default:
		return res, invalid("unknown drill depth %q", p.Depth)
	}
	res.OK = true
	return res, nil
}
