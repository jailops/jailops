package hostbackup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"jailops.net/jailops/internal/backup"
	"jailops.net/jailops/internal/repo/engine"
	"jailops.net/jailops/internal/sys/zfs"
	"jailops.net/jailops/sdk/api"
)

// Stream property names recorded in the manifest with every dataset. They are
// what restore needs to recreate the dataset below a new container.
const (
	propRel       = "jailops:rel"      // name below the container ("." for the container itself)
	propKind      = "jailops:kind"     // one of the Kind constants
	propMountRel  = "jailops:mountrel" // mountpoint relative to the container's mountpoint
	metaProject   = "project"          // manifest meta
	metaLayout    = "layout"           // thin or thick
	metaStorage   = "storage"          // storage name of the jail
	metaContainer = "container"        // the dataset name at backup time (informational)
	metaKind      = "kind"             // "jail" or "state"
	snapProp      = "jailops:backup"   // run id on the JailOps snapshot
	snapManaged   = "jailops:managed"  // ownership mark of the snapshot
	rootRel       = "."
)

// CaptureOptions describe one capture of one jail.
type CaptureOptions struct {
	// Policy is recorded in the manifest and the backup object.
	Policy string
	// Targets are BackupTarget names; empty means the default repository. The
	// jail is snapshotted ONCE and read once per target.
	Targets []string
	// FullEvery forces a full read when the last one is older (0 = default).
	FullEvery time.Duration
	// ForceFull reads every file (still deduplicated).
	ForceFull bool
	// RunID names the run in the catalog ("" = generated).
	RunID     string
	Scheduled bool
	Attempt   int
}

// TargetResult is the outcome of the upload to one target.
type TargetResult struct {
	Target string
	Run    Run
	Backup api.Backup
	Result *engine.BackupResult
	Err    error
}

// CaptureResult is the outcome of BackupJail.
type CaptureResult struct {
	// ZFSSnapshot is the name of the (since destroyed) consistent snapshot.
	ZFSSnapshot string
	Targets     []TargetResult
}

// Err joins the errors of the targets.
func (r *CaptureResult) Err() error {
	var errs []error
	for _, t := range r.Targets {
		if t.Err != nil {
			errs = append(errs, t.Err)
		}
	}
	return errors.Join(errs...)
}

// capturable returns the datasets of the record that are backed up, container
// first, then by mount depth (design 9.7: the thin root is not captured).
func capturable(j JailRecord) ([]Dataset, error) {
	thick := j.Spec.Layout == api.LayoutThick
	var out []Dataset
	hasContainer := false
	for _, d := range j.Datasets {
		switch {
		case d.Name == "":
			return nil, api.Errorf(api.CodeInvalid, "jail %q lists a dataset without a name", j.Name)
		case d.Kind == KindVolumes:
			continue
		case d.Kind == KindRoot && !thick && !j.IsState:
			continue
		}
		if d.Name == j.Container {
			hasContainer = true
		} else if !strings.HasPrefix(d.Name, j.Container+"/") {
			return nil, api.Errorf(api.CodeInvalid, "dataset %s is not below the container %s of jail %q", d.Name, j.Container, j.Name)
		}
		out = append(out, d)
	}
	if !hasContainer {
		return nil, api.Errorf(api.CodeInvalid, "jail %q lists no container dataset %s", j.Name, j.Container)
	}
	cmp := containerMountpoint(j)
	sort.SliceStable(out, func(a, b int) bool {
		da, db := mountDepth(out[a], j.Container, cmp), mountDepth(out[b], j.Container, cmp)
		if da != db {
			return da < db
		}
		return out[a].Name < out[b].Name
	})
	return out, nil
}

func containerMountpoint(j JailRecord) string {
	for _, d := range j.Datasets {
		if d.Name == j.Container {
			return d.Mountpoint
		}
	}
	return ""
}

func relName(container, name string) string {
	if name == container {
		return rootRel
	}
	return strings.TrimPrefix(name, container+"/")
}

func mountRel(cmp, mp string) (string, bool) {
	if cmp == "" || mp == "" || !strings.HasPrefix(mp, "/") {
		return "", false
	}
	r, err := filepath.Rel(cmp, mp)
	if err != nil || r == ".." || strings.HasPrefix(r, "../") {
		return "", false
	}
	return filepath.ToSlash(r), true
}

func mountDepth(d Dataset, container, cmp string) int {
	if d.Name == container {
		return 0
	}
	if r, ok := mountRel(cmp, d.Mountpoint); ok && r != "." {
		return 1 + strings.Count(r, "/")
	}
	return 1 + strings.Count(relName(container, d.Name), "/") + 50
}

// streamProps builds the properties recorded with a dataset's stream.
func streamProps(j JailRecord, d Dataset, cmp string) map[string]string {
	p := map[string]string{}
	for k, v := range d.Props {
		p[k] = v
	}
	p[propRel] = relName(j.Container, d.Name)
	p[propKind] = d.Kind
	if r, ok := mountRel(cmp, d.Mountpoint); ok {
		p[propMountRel] = r
	}
	return p
}

func (s *Service) snapshotName() string {
	return SnapshotPrefix + s.now().Format("20060102T150405") + "-" + randHex(2)
}

// BackupJail backs up one jail now: one atomic snapshot of its data datasets,
// read through .zfs/snapshot, uploaded to every target, the snapshot destroyed
// again whatever happens. A jail has at most one job at a time (api.CodeBusy).
// The jail is not stopped: the capture is crash-consistent unless Hooks quiesce
// the application.
func (s *Service) BackupJail(ctx context.Context, j JailRecord, o CaptureOptions) (out *CaptureResult, err error) {
	if j.UUID == "" || j.Name == "" {
		return nil, api.Errorf(api.CodeInvalid, "a jail record needs a name and a uuid")
	}
	if !s.acquire(j.UUID) {
		return nil, api.Errorf(api.CodeBusy, "a backup job for jail %q is already running", j.Name)
	}
	defer s.release(j.UUID)
	started := s.now()
	defer func() {
		// a run that failed before any upload still leaves a record: "the key is
		// not escrowed" must be visible to the operator
		if err == nil || (out != nil && len(out.Targets) > 0) {
			return
		}
		id := o.RunID
		if id == "" {
			id = s.newID("run")
		}
		run := Run{ID: id, Kind: RunBackup, Policy: o.Policy, Project: j.Project, Jail: j.Name, JailUUID: j.UUID, Scheduled: o.Scheduled,
			Attempt: o.Attempt, Started: started, Finished: s.now(), Error: err.Error(), Code: errCode(err)}
		_ = s.deps.Catalog.PutRun(context.WithoutCancel(ctx), run)
		if s.deps.Observer != nil {
			s.deps.Observer.RunFinished(context.WithoutCancel(ctx), run)
		}
	}()

	targets := o.Targets
	if len(targets) == 0 {
		targets = []string{""}
	}
	// Open every repository and check the key before touching ZFS.
	remotes := make([]Remote, len(targets))
	for i, t := range targets {
		r, err := s.remoteFor(ctx, t)
		if err != nil {
			return nil, err
		}
		if err := r.Refresh(ctx); err != nil {
			return nil, err
		}
		if _, err := s.requireKey(ctx, r); err != nil {
			return nil, err
		}
		remotes[i] = r
	}
	key := s.currentKey()
	sets, err := capturable(j)
	if err != nil {
		return nil, err
	}
	var bundle json.RawMessage
	if !j.IsState {
		b, err := backup.BuildBundle(j.Spec, j.Image, s.objectsFor(j.Project), j.SecretRefs)
		if err != nil {
			return nil, err
		}
		if bundle, err = json.Marshal(b); err != nil {
			return nil, err
		}
	}

	runID := o.RunID
	if runID == "" {
		runID = s.newID("run")
	}
	snap := s.snapshotName()
	res := &CaptureResult{ZFSSnapshot: snap}
	out = res
	var rev reverter
	defer func() {
		if rerr := rev.revert(ctx); rerr != nil {
			s.cfg.Log.Error("backup: cleaning up the snapshot failed", "jail", j.Name, "snapshot", snap, "err", rerr)
			err = errors.Join(err, fmt.Errorf("backup cleanup: %w", rerr))
		}
	}()

	// pre-hook, atomic snapshot, post-hook; the application is quiesced only
	// for the snapshot itself.
	hooked := false
	if h := s.deps.Hooks; h != nil {
		if err := s.fail("capture.pre"); err != nil {
			return nil, err
		}
		if err := h.Pre(ctx, j); err != nil {
			return nil, fmt.Errorf("pre-backup hook of %s: %w", j.Name, err)
		}
		hooked = true
	}
	var postErr error
	post := func() {
		if hooked {
			hooked = false
			postErr = s.deps.Hooks.Post(context.WithoutCancel(ctx), j)
		}
	}
	defer post()

	names := make([]string, len(sets))
	for i, d := range sets {
		names[i] = d.Name + "@" + snap
	}
	rev.add(func(ctx context.Context) error { return s.destroySnapshots(ctx, names) })
	serr := s.fail("capture.snapshot")
	if serr == nil {
		serr = s.zfs.Snapshot(ctx, zfs.SnapshotOpts{Names: names, Props: map[string]string{snapManaged: "on", snapProp: runID}})
	}
	post()
	if serr != nil {
		return nil, fmt.Errorf("snapshotting %s: %w", j.Name, serr)
	}
	var warnings []string
	if postErr != nil {
		warnings = append(warnings, "the post-backup hook failed: "+postErr.Error())
	}

	cmp := containerMountpoint(j)
	streams := make([]engine.Stream, len(sets))
	var closers []func()
	defer func() {
		for _, c := range closers {
			c()
		}
	}()
	for i, d := range sets {
		if err := s.fail("capture.read." + relName(j.Container, d.Name)); err != nil {
			return nil, err
		}
		full := names[i]
		guid, err := s.zfs.GUID(ctx, full)
		if err != nil {
			return nil, fmt.Errorf("reading the guid of %s: %w", full, err)
		}
		dir, err := s.deps.Mounter.SnapshotDir(ctx, d, snap)
		if err != nil {
			return nil, err
		}
		streams[i] = engine.Stream{Name: relName(j.Container, d.Name), Root: dir, ZFSSnapshot: snap, GUID: guid,
			CreateTxg: s.createTxg(ctx, full), Props: streamProps(j, d, cmp)}
		if s.deps.Agents != nil {
			// The daemon is not root: a file agent reads the snapshot for it and the tree is seen from there.
			fsys, closeFS, err := s.deps.Agents.ReadSnapshot(ctx, dir)
			if err != nil {
				return nil, fmt.Errorf("starting a file agent for %s: %w", streams[i].Name, err)
			}
			closers = append(closers, closeFS)
			streams[i].Root, streams[i].FS = "/", fsys
		}
	}

	meta := map[string]string{metaProject: j.Project, metaLayout: string(j.Spec.Layout), metaStorage: j.Spec.Storage,
		metaContainer: j.Container, metaKind: "jail"}
	if j.IsState {
		meta[metaKind] = "state"
	}
	for i, t := range targets {
		tr := s.upload(ctx, upload{j: j, o: o, key: key, remote: remotes[i], target: t, runID: runID, snap: snap, streams: streams,
			bundle: bundle, meta: meta, warnings: warnings, multi: len(targets) > 1})
		res.Targets = append(res.Targets, tr)
	}
	return res, res.Err()
}

type upload struct {
	j        JailRecord
	o        CaptureOptions
	key      interface{ DomainID() string }
	remote   Remote
	target   string
	runID    string
	snap     string
	streams  []engine.Stream
	bundle   json.RawMessage
	meta     map[string]string
	warnings []string
	multi    bool
}

// destroySnapshots removes the snapshots this package created. It only
// touches snapshots that exist and carry the JailOps ownership mark.
func (s *Service) destroySnapshots(ctx context.Context, names []string) error {
	var errs []error
	for _, n := range names {
		ok, err := s.zfs.Exists(ctx, n)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !ok {
			continue
		}
		p, err := s.zfs.Get(ctx, n, snapManaged)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if p[snapManaged].Value != "on" {
			continue // not ours
		}
		if err := s.zfs.Destroy(ctx, n, zfs.DestroyOpts{Defer: true}); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *Service) createTxg(ctx context.Context, snapshot string) uint64 {
	p, err := s.zfs.Get(ctx, snapshot, "createtxg")
	if err != nil {
		return 0
	}
	n, err := strconv.ParseUint(p["createtxg"].Value, 10, 64)
	if err != nil {
		return 0 // "-": a ZFS that does not report it
	}
	return n
}

// upload runs the engine for one target and records the result.
func (s *Service) upload(ctx context.Context, u upload) (tr TargetResult) {
	tr.Target = u.target
	id := u.runID
	if u.multi {
		id += "-" + u.target
	}
	run := Run{ID: id, Kind: RunBackup, Policy: u.o.Policy, Target: u.target, Project: u.j.Project, Jail: u.j.Name, JailUUID: u.j.UUID,
		Scheduled: u.o.Scheduled, Attempt: u.o.Attempt, Started: s.now(), ZFSSnap: u.snap}
	defer func() {
		run.Finished = s.now()
		if tr.Err != nil {
			run.OK, run.Error, run.Code = false, tr.Err.Error(), errCode(tr.Err)
		}
		tr.Run = run
		_ = s.deps.Catalog.PutRun(context.WithoutCancel(ctx), run)
		if s.deps.Observer != nil {
			s.deps.Observer.RunFinished(context.WithoutCancel(ctx), run)
		}
	}()
	if err := s.fail("capture.engine"); err != nil {
		tr.Err = err
		return
	}
	var bu *batchUploader
	newUp := func(ctx context.Context, group, snapshot, previous string) (engine.Uploader, error) {
		up, err := u.remote.NewUploader(ctx, group, snapshot, previous)
		if err != nil {
			return nil, err
		}
		bu = newBatchUploader(ctx, up)
		return bu, nil
	}
	eo := engine.BackupOptions{
		Key: s.currentKey(), Escrow: u.remote, RequireEscrow: !s.cfg.AllowUnescrowed,
		Host: s.cfg.HostUUID, Jail: u.j.UUID, JailName: u.j.Name, Policy: u.o.Policy,
		Streams: u.streams, Bundle: u.bundle, Meta: u.meta,
		Repo: u.remote, NewUploader: newUp,
		ForceFull: u.o.ForceFull, FullEvery: u.o.FullEvery, NoTrustCtime: s.cfg.NoTrustCtime,
		Workers: s.cfg.EngineWorkers, Now: s.cfg.Clock,
	}
	res, err := engine.Backup(ctx, eo)
	if err != nil {
		tr.Err = mapEngineErr(err)
		return
	}
	tr.Result = res
	b := s.backupObject(u.j, u.o, u.target, u.snap, res, u.bundle)
	if err := s.deps.Catalog.PutBackup(ctx, b); err != nil {
		s.cfg.Log.Error("backup: recording the backup failed", "snapshot", res.Snapshot, "err", err)
		res.Warnings = append(res.Warnings, "the backup is committed but could not be recorded in the local catalog: "+err.Error())
	}
	tr.Backup = b
	run.OK, run.Snapshot, run.Full = true, res.Snapshot, res.Full
	run.Bytes, run.NewBytes, run.Chunks = b.Status.Bytes, b.Status.NewBytes, b.Status.Chunks
	run.Files = b.Status.Files
	run.Warnings = append(append([]string(nil), u.warnings...), res.Warnings...)
	return
}

func (s *Service) backupObject(j JailRecord, o CaptureOptions, target, zsnap string, res *engine.BackupResult, bundle json.RawMessage) api.Backup {
	var rb api.RestoreBundle
	if len(bundle) > 0 {
		_ = json.Unmarshal(bundle, &rb)
	}
	var files int64
	for _, st := range res.Streams {
		files += st.Files
	}
	created := s.now()
	if res.Receipt != nil {
		created = res.Receipt.Committed
	}
	chunks := 0
	if res.Receipt != nil {
		chunks = res.Receipt.ChunkCount
	}
	return api.Backup{
		Metadata: api.ObjectMeta{Name: res.Snapshot, UUID: res.Snapshot, Project: j.Project, Host: s.cfg.HostUUID, Scope: api.ScopeHost,
			Generation: 1, CreatedAt: created, UpdatedAt: created},
		Spec: api.BackupSpec{Jail: j.Name, JailUUID: j.UUID, HostUUID: s.cfg.HostUUID, Policy: o.Policy, Target: target,
			Snapshot: res.Snapshot, ZFSSnapshot: zsnap, KeyDomain: s.currentKey().DomainID(), Full: res.Full, Bundle: rb},
		Status: api.BackupStatus{Bytes: int64(res.LogicalBytes), NewBytes: int64(res.UploadedBytes), Chunks: chunks, Files: files},
	}
}

// errCode returns the API code of an error ("" for a plain error).
func errCode(err error) api.Code {
	var ae *api.Error
	if errors.As(err, &ae) {
		return ae.Code
	}
	return ""
}

// mapEngineErr turns the engine's sentinel errors into API errors.
func mapEngineErr(err error) error {
	switch {
	case errors.Is(err, engine.ErrNotEscrowed):
		return api.Errorf(api.CodeKeyNotEscrowed, "%v", err)
	case errors.Is(err, engine.ErrNoKey):
		return noKeyErr()
	case errors.Is(err, engine.ErrWrongKey):
		return api.Errorf(api.CodeForbidden, "%v", err)
	}
	return err
}

// CleanupStale destroys leftover JailOps backup snapshots older than
// Config.StaleSnapshotAge (a crash between the snapshot and its destruction),
// except those of jobs that run now. It only touches snapshots named
// jailops-backup-* that carry jailops:managed=on.
func (s *Service) CleanupStale(ctx context.Context) (int, error) {
	list, err := s.zfs.List(ctx, zfs.ListOpts{Types: []string{"snapshot"}, Recursive: true, Props: []string{snapManaged, "creation"}})
	if err != nil {
		return 0, err
	}
	var errs []error
	n := 0
	for _, d := range list {
		_, sn := zfs.SplitSnapshot(d.Name)
		if !strings.HasPrefix(sn, SnapshotPrefix) || d.Props[snapManaged] != "on" {
			continue
		}
		if t := parseSnapTime(sn); !t.IsZero() && s.now().Sub(t) < s.cfg.StaleSnapshotAge {
			continue
		}
		if t := parseSnapTime(sn); t.IsZero() {
			continue // cannot tell its age: leave it
		}
		if err := s.zfs.Destroy(ctx, d.Name, zfs.DestroyOpts{Defer: true}); err != nil {
			errs = append(errs, err)
			continue
		}
		n++
	}
	return n, errors.Join(errs...)
}

// parseSnapTime reads the time out of a jailops-backup-<time>-<rand> name.
func parseSnapTime(name string) time.Time {
	rest := strings.TrimPrefix(name, SnapshotPrefix)
	if len(rest) < 15 {
		return time.Time{}
	}
	t, err := time.Parse("20060102T150405", rest[:15])
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// objectsFor returns the object source for a jail's project.
func (s *Service) objectsFor(project string) backup.ObjectSource {
	if po, ok := s.deps.Objects.(ProjectObjects); ok {
		return po.ForProject(project)
	}
	return s.deps.Objects
}
