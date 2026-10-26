package zfs

import (
	"context"
	"io"
	"sort"
	"strconv"
	"strings"

	"jailops.net/jailops/internal/sys/run"
	"jailops.net/jailops/sdk/api"
)

// Command is the zfs implementation of Client over a run.Runner.
type Command struct {
	R run.Runner
	// ZFS and Zpool override the binary names (default "zfs", "zpool").
	ZFS, Zpool string
}

var _ Client = (*Command)(nil)

// NewClient returns a Client that runs zfs and zpool through r.
func NewClient(r run.Runner) *Command { return &Command{R: r} }

func (c *Command) zfs() string {
	if c.ZFS != "" {
		return c.ZFS
	}
	return "zfs"
}

func (c *Command) zpool() string {
	if c.Zpool != "" {
		return c.Zpool
	}
	return "zpool"
}

func (c *Command) out(ctx context.Context, bin string, args ...string) (string, error) {
	res, err := c.R.Run(ctx, run.Cmd{Name: bin, Args: args})
	if err != nil {
		return "", mapError(err)
	}
	return string(res.Stdout), nil
}

func (c *Command) do(ctx context.Context, args ...string) error {
	_, err := c.out(ctx, c.zfs(), args...)
	return err
}

// propArgs renders -o prop=value pairs in sorted order (deterministic argv).
func propArgs(flag string, m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var a []string
	for _, k := range keys {
		a = append(a, flag, k+"="+m[k])
	}
	return a
}

// List implements Client.
func (c *Command) List(ctx context.Context, o ListOpts) ([]Dataset, error) {
	types := o.Types
	if len(types) == 0 {
		types = []string{"filesystem", "volume"}
	}
	for _, t := range types {
		switch t {
		case "filesystem", "volume", "snapshot", "bookmark":
		default:
			return nil, api.Errorf(api.CodeInvalid, "invalid zfs type %q", t)
		}
	}
	for _, p := range o.Props {
		if err := ValidateProperty(p); err != nil {
			return nil, err
		}
	}
	args := []string{"list", "-H", "-p", "-t", strings.Join(types, ",")}
	if o.Depth > 0 {
		args = append(args, "-d", itoa(o.Depth))
	} else if o.Recursive {
		args = append(args, "-r")
	}
	args = append(args, "-o", strings.Join(append([]string{"name", "type"}, o.Props...), ","))
	if o.Root != "" {
		if _, err := ValidateAny(o.Root); err != nil {
			return nil, err
		}
		args = append(args, o.Root)
	}
	out, err := c.out(ctx, c.zfs(), args...)
	if err != nil {
		return nil, err
	}
	return ParseList(out, o.Props)
}

func itoa(n int) string { return strconv.Itoa(n) }

// Get implements Client.
func (c *Command) Get(ctx context.Context, name string, props ...string) (map[string]Prop, error) {
	if _, err := ValidateAny(name); err != nil {
		return nil, err
	}
	if len(props) == 0 {
		props = []string{"all"}
	}
	for _, p := range props {
		if p != "all" {
			if err := ValidateProperty(p); err != nil {
				return nil, err
			}
		}
	}
	out, err := c.out(ctx, c.zfs(), "get", "-H", "-p", "-o", "property,value,source", strings.Join(props, ","), name)
	if err != nil {
		return nil, err
	}
	return ParseGet(out)
}

// Exists implements Client.
func (c *Command) Exists(ctx context.Context, name string) (bool, error) {
	if _, err := ValidateAny(name); err != nil {
		return false, err
	}
	_, err := c.Get(ctx, name, "name")
	if err == nil {
		return true, nil
	}
	if IsNotFound(err) {
		return false, nil
	}
	return false, err
}

// Set implements Client. Properties are set in one call.
func (c *Command) Set(ctx context.Context, name string, props map[string]string) error {
	if _, err := ValidateAny(name); err != nil {
		return err
	}
	if len(props) == 0 {
		return nil
	}
	if err := validateProps(props); err != nil {
		return err
	}
	args := []string{"set"}
	for _, kv := range propArgs("-o", props) {
		if kv != "-o" {
			args = append(args, kv)
		}
	}
	return c.do(ctx, append(args, name)...)
}

// Inherit implements Client.
func (c *Command) Inherit(ctx context.Context, name string, props ...string) error {
	if _, err := ValidateAny(name); err != nil {
		return err
	}
	for _, p := range props {
		if err := ValidateProperty(p); err != nil {
			return err
		}
		if err := c.do(ctx, "inherit", p, name); err != nil {
			return err
		}
	}
	return nil
}

// Create implements Client.
func (c *Command) Create(ctx context.Context, o CreateOpts) error {
	if err := ValidateDataset(o.Name); err != nil {
		return err
	}
	if err := validateProps(o.Props); err != nil {
		return err
	}
	args := []string{"create"}
	if o.Parents {
		args = append(args, "-p")
	}
	if o.VolumeSize != "" {
		if !validSize(o.VolumeSize) {
			return api.Errorf(api.CodeInvalid, "invalid volume size %q", o.VolumeSize)
		}
		args = append(args, "-V", o.VolumeSize)
	}
	args = append(args, propArgs("-o", o.Props)...)
	return c.do(ctx, append(args, o.Name)...)
}

func validSize(s string) bool {
	if s == "" || len(s) > 20 {
		return false
	}
	for i := 0; i < len(s); i++ {
		ch := s[i]
		digit := ch >= '0' && ch <= '9'
		last := i == len(s)-1
		unit := strings.IndexByte("KMGTPEkmgtpe", ch) >= 0
		if !(digit || (last && unit && i > 0)) {
			return false
		}
	}
	return true
}

// Clone implements Client.
func (c *Command) Clone(ctx context.Context, snapshot, target string, props map[string]string, parents bool) error {
	if err := ValidateSnapshot(snapshot); err != nil {
		return err
	}
	if err := ValidateDataset(target); err != nil {
		return err
	}
	if err := validateProps(props); err != nil {
		return err
	}
	args := []string{"clone"}
	if parents {
		args = append(args, "-p")
	}
	args = append(args, propArgs("-o", props)...)
	return c.do(ctx, append(args, snapshot, target)...)
}

// Snapshot implements Client: one atomic zfs snapshot call for all names.
func (c *Command) Snapshot(ctx context.Context, o SnapshotOpts) error {
	if len(o.Names) == 0 {
		return api.Errorf(api.CodeInvalid, "no snapshots requested")
	}
	for _, n := range o.Names {
		if err := ValidateSnapshot(n); err != nil {
			return err
		}
	}
	if err := validateProps(o.Props); err != nil {
		return err
	}
	args := []string{"snapshot"}
	if o.Recursive {
		args = append(args, "-r")
	}
	args = append(args, propArgs("-o", o.Props)...)
	return c.do(ctx, append(args, o.Names...)...)
}

// Destroy implements Client.
func (c *Command) Destroy(ctx context.Context, name string, o DestroyOpts) error {
	kind, err := ValidateAny(name)
	if err != nil {
		return err
	}
	if o.Defer && kind != KindSnapshot {
		return api.Errorf(api.CodeInvalid, "deferred destroy applies to snapshots only")
	}
	args := []string{"destroy"}
	if o.Defer {
		args = append(args, "-d")
	}
	if o.Recursive {
		args = append(args, "-r")
	}
	if o.Dependents {
		args = append(args, "-R")
	}
	return c.do(ctx, append(args, name)...)
}

// Rename implements Client.
func (c *Command) Rename(ctx context.Context, from, to string, o RenameOpts) error {
	k1, err := ValidateAny(from)
	if err != nil {
		return err
	}
	k2, err := ValidateAny(to)
	if err != nil {
		return err
	}
	if k1 != k2 || k1 == KindBookmark {
		return api.Errorf(api.CodeInvalid, "rename needs two datasets or two snapshots")
	}
	args := []string{"rename"}
	if o.Parents {
		args = append(args, "-p")
	}
	if o.Recursive {
		args = append(args, "-r")
	}
	return c.do(ctx, append(args, from, to)...)
}

// Rollback implements Client.
func (c *Command) Rollback(ctx context.Context, snapshot string, o RollbackOpts) error {
	if err := ValidateSnapshot(snapshot); err != nil {
		return err
	}
	args := []string{"rollback"}
	if o.DestroyClones {
		args = append(args, "-R")
	} else if o.DestroyNewer {
		args = append(args, "-r")
	}
	return c.do(ctx, append(args, snapshot)...)
}

func (c *Command) holdCmd(ctx context.Context, verb, tag string, snaps []string) error {
	if err := ValidateTag(tag); err != nil {
		return err
	}
	if len(snaps) == 0 {
		return api.Errorf(api.CodeInvalid, "no snapshots given")
	}
	for _, s := range snaps {
		if err := ValidateSnapshot(s); err != nil {
			return err
		}
	}
	return c.do(ctx, append([]string{verb, tag}, snaps...)...)
}

// Hold implements Client.
func (c *Command) Hold(ctx context.Context, tag string, snaps ...string) error {
	return c.holdCmd(ctx, "hold", tag, snaps)
}

// Release implements Client.
func (c *Command) Release(ctx context.Context, tag string, snaps ...string) error {
	return c.holdCmd(ctx, "release", tag, snaps)
}

// Holds implements Client.
func (c *Command) Holds(ctx context.Context, snapshot string) ([]Hold, error) {
	if err := ValidateSnapshot(snapshot); err != nil {
		return nil, err
	}
	out, err := c.out(ctx, c.zfs(), "holds", "-H", "-p", snapshot)
	if err != nil {
		return nil, err
	}
	return ParseHolds(out)
}

// Bookmark implements Client.
func (c *Command) Bookmark(ctx context.Context, snapshot, bookmark string) error {
	if err := ValidateSnapshot(snapshot); err != nil {
		return err
	}
	if err := ValidateBookmark(bookmark); err != nil {
		return err
	}
	return c.do(ctx, "bookmark", snapshot, bookmark)
}

func (c *Command) uintProp(ctx context.Context, name, prop string) (uint64, error) {
	p, err := c.Get(ctx, name, prop)
	if err != nil {
		return 0, err
	}
	v, ok := p[prop]
	if !ok {
		return 0, api.Errorf(api.CodeInternal, "zfs get returned no %s for %s", prop, name)
	}
	return ParseUint(v.Value)
}

// Written implements Client.
func (c *Command) Written(ctx context.Context, dataset, since string) (uint64, error) {
	if err := ValidateDataset(dataset); err != nil {
		return 0, err
	}
	prop := "written"
	if since != "" {
		if err := ValidateSnapshot(since); err != nil {
			return 0, err
		}
		_, short := SplitSnapshot(since)
		prop = "written@" + short
	}
	return c.uintProp(ctx, dataset, prop)
}

// GUID implements Client.
func (c *Command) GUID(ctx context.Context, name string) (uint64, error) {
	if _, err := ValidateAny(name); err != nil {
		return 0, err
	}
	return c.uintProp(ctx, name, "guid")
}

// Clones implements Client.
func (c *Command) Clones(ctx context.Context, snapshot string) ([]string, error) {
	if err := ValidateSnapshot(snapshot); err != nil {
		return nil, err
	}
	p, err := c.Get(ctx, snapshot, "clones")
	if err != nil {
		return nil, err
	}
	return ParseClones(p["clones"].Value), nil
}

// Origin implements Client.
func (c *Command) Origin(ctx context.Context, dataset string) (string, error) {
	if err := ValidateDataset(dataset); err != nil {
		return "", err
	}
	p, err := c.Get(ctx, dataset, "origin")
	if err != nil {
		return "", err
	}
	if v := p["origin"].Value; v != "-" {
		return v, nil
	}
	return "", nil
}

// Send implements Client. The stream goes straight from the child's stdout
// to w. There is no command timeout; cancel ctx to abort.
func (c *Command) Send(ctx context.Context, o SendOpts, w io.Writer) error {
	args := []string{"send"}
	if o.ResumeToken != "" {
		if err := ValidateResumeToken(o.ResumeToken); err != nil {
			return err
		}
		args = append(args, "-t", o.ResumeToken)
	} else {
		if err := ValidateSnapshot(o.Snapshot); err != nil {
			return err
		}
		if o.Raw {
			args = append(args, "-w")
		}
		if o.Compressed {
			args = append(args, "-c")
		}
		if o.LargeBlock {
			args = append(args, "-L")
		}
		if o.Replicate {
			args = append(args, "-R")
		}
		if o.From != "" {
			if k, err := ValidateAny(o.From); err != nil || k == KindDataset {
				return api.Errorf(api.CodeInvalid, "invalid incremental source %q", o.From)
			}
			if o.Intermediary {
				args = append(args, "-I", o.From)
			} else {
				args = append(args, "-i", o.From)
			}
		}
		args = append(args, o.Snapshot)
	}
	_, err := c.R.Run(ctx, run.Cmd{Name: c.zfs(), Args: args, Stdout: w, Timeout: run.NoTimeout})
	return mapError(err)
}

// Receive implements Client. -u is always used unless o.Mount is set; -F
// only when o.Force is set.
func (c *Command) Receive(ctx context.Context, o ReceiveOpts, r io.Reader) error {
	if err := ValidateDataset(o.Target); err != nil {
		return err
	}
	if err := validateProps(o.Props); err != nil {
		return err
	}
	args := []string{"receive"}
	if !o.Mount {
		args = append(args, "-u")
	}
	if o.Resumable {
		args = append(args, "-s")
	}
	if o.Force {
		args = append(args, "-F")
	}
	args = append(args, propArgs("-o", o.Props)...)
	for _, x := range o.Exclude {
		if err := ValidateProperty(x); err != nil {
			return err
		}
		args = append(args, "-x", x)
	}
	args = append(args, o.Target)
	_, err := c.R.Run(ctx, run.Cmd{Name: c.zfs(), Args: args, Stdin: r, Timeout: run.NoTimeout})
	return mapError(err)
}

// ReceiveAbort implements Client.
func (c *Command) ReceiveAbort(ctx context.Context, dataset string) error {
	if err := ValidateDataset(dataset); err != nil {
		return err
	}
	return c.do(ctx, "receive", "-A", dataset)
}

// ListPools implements Client.
func (c *Command) ListPools(ctx context.Context) ([]Pool, error) {
	out, err := c.out(ctx, c.zpool(), "list", "-H", "-p", "-o", "name,size,allocated,free,health")
	if err != nil {
		return nil, err
	}
	return ParsePools(out)
}

// ActiveFeatures implements Client.
func (c *Command) ActiveFeatures(ctx context.Context, pool string) ([]string, error) {
	if err := ValidatePool(pool); err != nil {
		return nil, err
	}
	out, err := c.out(ctx, c.zpool(), "get", "-H", "-p", "-o", "property,value", "all", pool)
	if err != nil {
		return nil, err
	}
	return ParseActiveFeatures(out)
}
