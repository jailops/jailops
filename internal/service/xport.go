package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"jailops.net/jailops/internal/backup"
	"jailops.net/jailops/internal/jailxfer"
	"jailops.net/jailops/internal/lifecycle"
	"jailops.net/jailops/internal/validate"
	"jailops.net/jailops/sdk/api"
)

// The export and import of a jail (extension jail_export, docs/export-import.md): a jail leaves this host as a file
// and comes back, here or on another host, as a new jail of a project the caller may use.

// importRequest is what recovery needs to take away what an import that did not finish made.
type importRequest struct {
	UUID    string   `json:"uuid"`
	Volumes []string `json:"volumes,omitempty"`
}

// ExportJail writes the export file of a jail to w. The jail keeps running; it is busy (no stop, no upgrade) until the
// last byte is written, so the datasets cannot go away under the stream. The reply says what was sent, for the audit.
func (s *Service) ExportJail(ctx context.Context, name string, req api.JailExportRequest, w io.Writer) (string, error) {
	if err := s.requireHost("export"); err != nil {
		return "", err
	}
	if s.life == nil {
		return "", api.Errorf(api.CodeUnsupported, "this daemon has no host role")
	}
	p, key, err := s.jailRef(ctx, name)
	if err != nil {
		return "", err
	}
	j, err := s.jailSnapshot(key)
	if err != nil {
		return "", notFound("jail", name)
	}
	if req.Snapshot != "" {
		if err := validate.SnapshotName("snapshot", req.Snapshot); err != nil {
			return "", err
		}
	}
	unlock, err := s.locks.lockPatient(ctx, "export jail "+name, jailLock(key))
	if err != nil {
		return "", err
	}
	defer unlock()
	plan, err := s.life.PlanExport(ctx, key, req.Snapshot, req.WithVolumes)
	if err != nil {
		return "", err
	}
	lay, err := s.life.Layout(ctx, key)
	if err != nil {
		return "", err
	}
	proj := p.Meta.Name
	gen := api.ImageGeneration{Number: lay.Generation, Name: genRef(lay.Image, lay.Generation), Manifest: lay.Manifest}
	if gen.Manifest == "" {
		gen.Manifest = "-"
	}
	m := api.ExportManifest{Created: s.now().UTC(), Name: j.Metadata.Name, Project: proj, UUID: j.Metadata.UUID, Labels: j.Metadata.Labels,
		Layout: j.Spec.Layout, OS: j.Spec.OS, Warnings: plan.Warnings}
	m.Image = api.ExportImage{Name: lay.Image, Generation: lay.Generation, Manifest: lay.Manifest}
	if im, err := s.imgKind.get(lay.Image); err == nil {
		m.Image.Release = im.Spec.Release
	}
	if si, ok := s.storageOfJail(key); ok && lay.Image != "" && lay.Generation > 0 {
		if g, err := s.life.Images.Resolve(ctx, si.Root, genRef(lay.Image, lay.Generation)); err == nil {
			m.Image.FreeBSDVersion = g.FreeBSDVersion
		}
	}
	var refs []string
	for k, v := range s.effective(j).Env {
		if isSealed(v) {
			refs = append(refs, k)
		}
	}
	sort.Strings(refs)
	if m.Bundle, err = backup.BuildBundle(j.Spec, gen, s.BackupObjects(proj), refs); err != nil {
		return "", err
	}
	if len(j.Spec.Profiles) > 0 {
		m.Warnings = append(m.Warnings, "the jail uses the profiles "+strings.Join(j.Spec.Profiles, ", ")+": the project that imports it needs profiles of the same names")
	}
	stamp := s.stamp()
	snapName := lifecycle.ExportSnapName(stamp)
	for _, x := range plan.Streams {
		for _, part := range x.Parts {
			if part.Snapshot == "" {
				part.Snapshot = x.Snapshot
			}
			if part.Snapshot == "" {
				part.Snapshot = snapName
			}
			if req.Zstd {
				part.Compression = "zstd"
			}
			m.Parts = append(m.Parts, part)
		}
		if x.Volume == "" {
			continue
		}
		o, err := s.volKind.p(proj).get(x.Volume)
		if err != nil {
			return "", err
		}
		ev := api.ExportVolume{Name: x.Volume, Spec: o.Spec, Labels: o.Meta.Labels, Shared: len(s.volumeUses(proj, x.Volume)) > 1}
		if ev.Shared {
			m.Warnings = append(m.Warnings, "the volume "+x.Volume+" is attached to other jails too: the file holds a copy of it, and the import makes a volume of its own")
		}
		m.Volumes = append(m.Volumes, ev)
	}
	xw, err := jailxfer.NewWriter(w, m)
	if err != nil {
		return "", err
	}
	next := 0
	err = s.life.ExportJail(ctx, plan, stamp, func(part api.ExportPart) (io.WriteCloser, error) {
		if next >= len(m.Parts) {
			return nil, fmt.Errorf("export: more parts than the manifest lists")
		}
		mp := m.Parts[next]
		next++
		if mp.Dataset != part.Dataset || mp.Snapshot != part.Snapshot || mp.From != part.From {
			return nil, fmt.Errorf("export: part %s does not match the manifest", part.Dataset)
		}
		return xw.Part(mp)
	})
	if err != nil {
		return "", err
	}
	if err := xw.Close(); err != nil {
		return "", err
	}
	return fmt.Sprintf("jail=%s project=%s parts=%d volumes=%d", j.Metadata.Name, proj, len(m.Parts), len(m.Volumes)), nil
}

// importWork is a checked import request: the spec of the new jail and what it needs.
type importWork struct {
	name, key string
	spec      api.JailSpec
	vols      []importVolume
	image     api.ImageGeneration
	pf        api.RestorePreflight
}

type importVolume struct {
	src, dst string
	spec     api.VolumeSpec
	labels   map[string]string
	mode     uint32
}

func jsonCopy[T any](v T) T {
	b, _ := json.Marshal(v)
	var out T
	_ = json.Unmarshal(b, &out)
	return out
}

// checkImport decides what an import of the file described by m would do in project p, and collects what is missing,
// what would change and what stops it. It changes nothing.
func (s *Service) checkImport(ctx context.Context, p project, m api.ExportManifest, req api.JailImportRequest) *importWork {
	w := &importWork{}
	var missing, changes, errs []string
	fail := func(f string, a ...any) { errs = append(errs, fmt.Sprintf(f, a...)) }
	chg := func(f string, a ...any) { changes = append(changes, fmt.Sprintf(f, a...)) }
	miss := func(f string, a ...any) { missing = append(missing, fmt.Sprintf(f, a...)) }
	defer func() {
		sort.Strings(missing)
		w.pf = api.RestorePreflight{OK: len(errs) == 0, Missing: missing, Changes: changes, Errors: errs}
	}()
	proj := p.Meta.Name
	if m.Format != api.ExportFormat {
		fail("the file has format %d; this version reads format %d", m.Format, api.ExportFormat)
		return w
	}
	name := req.As
	if name == "" {
		name = m.Name
	}
	w.name = name
	if err := validate.JailName(name); err != nil {
		fail("%v", err)
		return w
	}
	key, err := validate.QualifiedName(proj, name)
	if err != nil {
		fail("%v", err)
		return w
	}
	w.key = key
	s.mu.RLock()
	_, exists := s.jails[key]
	s.mu.RUnlock()
	if exists {
		fail("a jail named %q already exists in project %s: import it with --as NAME", name, proj)
	} else {
		chg("create jail %s in project %s", name, proj)
	}
	chg("the new jail gets a new uuid and does not start at boot")
	spec := jsonCopy(m.Bundle.Spec)
	spec.Layout, spec.OS = m.Layout, m.OS
	spec.Boot.Autostart = false
	if spec.Hostname == m.Name && name != m.Name {
		spec.Hostname = ""
	}
	// networks: the file names networks of the exporting host
	for i := range spec.Network {
		a := &spec.Network[i]
		if to, ok := req.NetworkMap[a.Network]; ok {
			a.Network = to
		}
		if !s.netKind.exists(a.Network) {
			fail("network %q does not exist on this host: import with --network-map %s=NETWORK", a.Network, a.Network)
		} else if _, ok := specGrantsNetwork(p.Spec, a.Network); !ok {
			fail("network %q is not granted to project %s: jailops network grant %s --project %s", a.Network, proj, a.Network, proj)
		}
		for _, v := range []*string{&a.IPv4, &a.IPv6} {
			if *v == "" || *v == "none" || *v == "auto" {
				continue
			}
			if !req.KeepIP {
				*v = "auto"
				continue
			}
			if conflict := s.addressInUse(a.Network, *v); conflict != "" {
				fail("%s: address %s on %s is used by jail %q (import without --keep-ip to get a new address)", api.CodeIPConflict, *v, a.Network, conflict)
			} else {
				chg("keep address %s on %s", *v, a.Network)
			}
		}
		a.MAC = ""
	}
	for _, k := range sortedMapKeys(req.NetworkMap) {
		if !s.netKind.exists(req.NetworkMap[k]) {
			fail("network-map sends %q to %q, which does not exist on this host", k, req.NetworkMap[k])
		}
	}
	// storage: the file's, when this project may use it, else the project's only one
	if st, ok := s.storage(spec.Storage); !ok || !specGrantsStorage(p.Spec, spec.Storage) {
		if sole := p.Spec.SoleStorage(); sole != "" {
			if spec.Storage != "" {
				chg("storage %q is not available to project %s: the jail goes on %s", spec.Storage, proj, sole)
			}
			spec.Storage = sole
		} else {
			spec.Storage = ""
			fail("the file names storage %q, which project %s cannot use, and the project has no single storage: name one with --storage NAME", m.Bundle.Spec.Storage, proj)
		}
	} else {
		_ = st
	}
	// the image of a thin jail has to be here; a thick jail carries its root
	w.image = api.ImageGeneration{Number: m.Image.Generation, Name: genRef(m.Image.Name, m.Image.Generation), Manifest: m.Image.Manifest}
	if m.Layout == api.LayoutThin {
		spec.Image = m.Image.Name
		switch im, _, err := s.imageFor(spec); {
		case err != nil:
			miss("image %s", m.Image.Name)
			fail("the jail's root is made from the image %s, which this host does not have: jailops image create %s --release %s, then jailops image build %s", m.Image.Name, m.Image.Name, m.Image.Release, m.Image.Name)
		case m.Image.Release != "" && im.Spec.Release != "" && im.Spec.Release != m.Image.Release:
			fail("image %s here is %s, the jail was exported from %s: move it to the newer release with jailops jail upgrade --image after the import, or import it on a host of the same release", m.Image.Name, im.Spec.Release, m.Image.Release)
		default:
			if si, ok := s.storage(spec.Storage); ok && s.life != nil {
				g, gerr := s.life.Images.Resolve(ctx, si.Root, m.Image.Name)
				if gerr != nil {
					miss("a generation of image %s", m.Image.Name)
					fail("image %s has no generation on storage %s: jailops image build %s", m.Image.Name, spec.Storage, m.Image.Name)
				} else {
					w.image = g
					if g.Manifest != m.Image.Manifest || g.Number != m.Image.Generation {
						chg("the root is made from %s here (%s), the exported jail ran on generation g%d (%s): the data is the same, the base system is this host's", g.Name, firstNonEmptyStr(g.FreeBSDVersion, "FreeBSD version not recorded"), m.Image.Generation, firstNonEmptyStr(m.Image.FreeBSDVersion, "FreeBSD version not recorded"))
					}
				}
			}
		}
	} else if _, err := s.imgKind.get(m.Image.Name); err != nil {
		chg("image %s is not on this host: the thick jail carries its root, and `jail upgrade` needs the image", m.Image.Name)
	}
	// the objects the jail refers to
	for i, n := range m.Bundle.PolicyNames {
		if hp, err := s.polKind.p(proj).get(n); err != nil {
			miss("firewall policy %s", n)
			chg("create firewall policy %s from the file", n)
		} else if i < len(m.Bundle.Policies) && !jsonEqual(hp.Spec, m.Bundle.Policies[i]) {
			chg("firewall policy %s exists with other rules; the existing policy is used", n)
		}
	}
	for i, n := range m.Bundle.AddressSetNames {
		if hs, err := s.setKind.p(proj).get(n); err != nil {
			miss("address set %s", n)
			chg("create address set %s from the file", n)
		} else if i < len(m.Bundle.AddressSets) && !jsonEqual(hs.Spec, m.Bundle.AddressSets[i]) {
			chg("address set %s exists with other members; the existing set is used", n)
		}
	}
	// volumes the file carries become volumes of the project; attachments to volumes it does not carry are dropped
	inFile := map[string]api.ExportVolume{}
	for _, v := range m.Volumes {
		inFile[v.Name] = v
	}
	taken := map[string]bool{}
	for _, v := range m.Volumes {
		dst := v.Name
		if to, ok := req.VolumeMap[v.Name]; ok {
			dst = to
		}
		if taken[dst] {
			fail("two volumes would be named %q", dst)
		}
		taken[dst] = true
		vs := v.Spec
		vs.Storage = spec.Storage
		cs, _, mode, err := s.checkVolumeSpec(p, dst, vs)
		if err != nil {
			fail("volume %s: %v", v.Name, err)
			continue
		}
		if s.volKind.p(proj).exists(dst) {
			fail("volume %q already exists in project %s: import with --volume-map %s=OTHER", dst, proj, v.Name)
			continue
		}
		w.vols = append(w.vols, importVolume{src: v.Name, dst: dst, spec: cs, labels: v.Labels, mode: mode})
		if v.Spec.Encrypted {
			chg("volume %s is made as an encrypted volume and stays locked: unlock it with its passphrase", dst)
		} else {
			chg("make volume %s from the file", dst)
		}
	}
	var keep []api.VolumeAttach
	for _, a := range spec.Volumes {
		if _, ok := inFile[a.Name]; !ok {
			chg("volume %s is not in the file: the attachment at %s is dropped; attach a volume with jailops volume attach", a.Name, a.Mount)
			continue
		}
		if to, ok := req.VolumeMap[a.Name]; ok {
			a.Name = to
		}
		keep = append(keep, a)
	}
	spec.Volumes = keep
	w.spec = spec
	if req.Start {
		chg("start the jail")
	}
	return w
}

func firstNonEmptyStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// addressInUse says which jail holds addr on a network ("" when it is free).
func (s *Service) addressInUse(network, addr string) string {
	want, err := netip.ParseAddr(addr)
	if err != nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, k := range sortedKeys(s.jails) {
		j := s.jails[k].jail
		for _, at := range j.Spec.Network {
			if at.Network != network {
				continue
			}
			for _, v := range []string{at.IPv4, at.IPv6} {
				if a, err := netip.ParseAddr(v); err == nil && a == want {
					return jailDisplay(j.Metadata.Project, j.Metadata.Name)
				}
			}
		}
	}
	return ""
}

// ImportPreflight says what an import of the file with this manifest would do, and what stops it. It changes nothing.
func (s *Service) ImportPreflight(ctx context.Context, in api.JailImportPreflight) (api.RestorePreflight, error) {
	if err := s.requireHost("import"); err != nil {
		return api.RestorePreflight{}, err
	}
	p, err := s.access(ctx)
	if err != nil {
		return api.RestorePreflight{}, err
	}
	if err := bodyProject(p.Meta.Name, in.Request.Project); err != nil {
		return api.RestorePreflight{}, err
	}
	w := s.checkImport(ctx, p, in.Manifest, in.Request)
	if len(w.pf.Errors) == 0 {
		// the checks a new jail goes through when it is made: grants, quotas, names and the rest
		spec := w.spec
		if release, aerr := s.admit(ctx, p, w.key, &spec, nil); aerr != nil {
			w.pf.Errors = append(w.pf.Errors, aerr.Error())
			w.pf.OK = false
		} else {
			release()
			if aerr := s.checkNewJail(p.Meta.Name, w.name, spec); aerr != nil {
				w.pf.Errors = append(w.pf.Errors, aerr.Error())
				w.pf.OK = false
			}
		}
	}
	return w.pf, nil
}

// ImportJail reads an export file from rd and makes a new jail of it in the project of the request. The jail is
// registered when the last dataset has arrived and its checksums have been verified; a file that is cut off or damaged
// leaves nothing behind. The file is kept on the host (<state dir>/imports) while it arrives; the call answers when it has
// arrived and the checks that need no dataset have passed, with the pending operation and the name of the jail. The
// operation makes the jail: its metadata is the api.JailImported, and the error of a failed import is its error. A client
// that goes away after the upload does not stop it.
func (s *Service) ImportJail(ctx context.Context, req api.JailImportRequest, rd io.Reader) (api.Operation, string, error) {
	if err := s.requireHost("import"); err != nil {
		return api.Operation{}, "", err
	}
	p, err := s.access(ctx)
	if err != nil {
		return api.Operation{}, "", err
	}
	if err := bodyProject(p.Meta.Name, req.Project); err != nil {
		return api.Operation{}, "", err
	}
	// The file is kept on the host before anything else: the request ends when it has arrived, the operation reads it
	// from there, and a client that goes away after the upload does not stop the import.
	staged, err := s.stageImport(rd)
	if err != nil {
		return api.Operation{}, "", err
	}
	stagedOK := false
	defer func() {
		if !stagedOK {
			staged.discard()
		}
	}()
	xr, err := jailxfer.NewReader(staged.f)
	if err != nil {
		return api.Operation{}, "", invalid("%v", err)
	}
	m := xr.Manifest()
	w := s.checkImport(ctx, p, m, req)
	if !w.pf.OK {
		return api.Operation{}, "", api.Errorf(api.CodeInvalid, "the import cannot be done: %s", strings.Join(w.pf.Errors, "; ")).
			WithDetail("errors", strings.Join(w.pf.Errors, "; ")).WithDetail("missing", strings.Join(w.pf.Missing, "; "))
	}
	proj, name, key := p.Meta.Name, w.name, w.key
	spec := w.spec
	spec.Network = append([]api.NetworkAttach(nil), spec.Network...)
	attach := spec.Volumes
	spec.Volumes = nil // attached when the volumes exist
	if err := s.validateJail(proj, name, spec); err != nil {
		return api.Operation{}, "", err
	}
	release, err := s.admit(ctx, p, key, &spec, nil)
	if err != nil {
		return api.Operation{}, "", err
	}
	submitted := false
	defer func() {
		if !submitted {
			release()
		}
	}()
	if err := canonicalJailSizes(&spec); err != nil {
		return api.Operation{}, "", err
	}
	if err := s.checkNewJail(proj, name, spec); err != nil {
		return api.Operation{}, "", err
	}
	if attach, err = s.lcfg.CheckVolumes(attach); err != nil {
		return api.Operation{}, "", err
	}
	if spec.Hostname == "" {
		spec.Hostname = name
	}
	meta := api.ObjectMeta{Name: name, Project: proj, UUID: state_uuid(), Scope: api.ScopeHost, Host: s.d.HostUUID, Generation: 1,
		Labels: m.Labels, CreatedAt: s.now().UTC(), UpdatedAt: s.now().UTC()}
	if err := api.ValidLabels(meta.Labels); err != nil {
		meta.Labels = nil
	}
	locks := append([]string{jailLock(key)}, s.netLocksEff(proj, spec)...)
	for _, v := range w.vols {
		locks = append(locks, volumeLock(proj, v.dst))
	}
	var warnings []string
	op, err := s.Submit(ctx, OpSpec{
		Action: api.OpJailImport, Description: "Import jail " + name, Locks: locks, Project: proj,
		Resources: map[string][]string{"jails": {"/api/v1/jails/" + name}},
		Run: func(ctx context.Context, h *OpHandle) (any, error) {
			defer release()
			defer staged.discard()
			s.place(key, spec.Storage)
			ok := false
			var undos []func(context.Context) error
			defer func() {
				if ok {
					return
				}
				uctx := context.WithoutCancel(ctx)
				for i := len(undos) - 1; i >= 0; i-- {
					if err := undos[i](uctx); err != nil {
						s.log.Warn("import: cleanup failed", "jail", key, "err", err)
					}
				}
				s.unplace(key)
			}()
			si, _ := s.storage(spec.Storage)
			g, _ := specStorageGrant(p.Spec, spec.Storage)
			if err := s.ensureProjectDatasets(ctx, proj, si, g); err != nil {
				return nil, err
			}
			// policies and address sets the file brings; networks are never made here
			b := m.Bundle
			b.NetworkNames, b.Networks = nil, nil
			undo, err := s.RestoreEnsureObjects(ctx, proj, b, req.NetworkMap)
			if undo != nil {
				undos = append(undos, undo)
			}
			if err != nil {
				return nil, err
			}
			if err := s.assignOwn(proj, key, &spec); err != nil {
				return nil, err
			}
			ireq := &importRequest{UUID: meta.UUID}
			bind := &opRequest{Kind: "import", Jail: key, Import: ireq}
			if err := h.Bind("import/"+key, bind); err != nil {
				return nil, err
			}
			jail := api.Jail{Metadata: meta, Spec: spec}
			jail.Spec.Volumes = attach
			jail.Status = api.JailStatus{Power: api.PowerStopped}
			var volErr error
			in := lifecycle.ImportInput{Jail: jail, Image: w.image, OS: string(spec.OS)}
			in.Volume = func(ctx context.Context, part api.ExportPart, r io.Reader) error {
				var v *importVolume
				for i := range w.vols {
					if w.vols[i].src == part.Dataset {
						v = &w.vols[i]
					}
				}
				if v == nil {
					return invalid("the file carries a volume %q that was not planned", part.Dataset)
				}
				// a volume of that name that is already there is not ours to undo: the check comes before the undo is armed
				if err := s.volKind.p(proj).notTaken(v.dst); err != nil {
					volErr = err
					return err
				}
				ireq.Volumes = append(ireq.Volumes, v.dst)
				_ = s.journal.putRequest("import/"+key, opRequest{Kind: "import", Jail: key, Import: ireq})
				undos = append(undos, func(ctx context.Context) error { return s.dropVolume(ctx, proj, v.dst) })
				volErr = s.receiveVolume(ctx, p, si, meta, *v, r)
				return volErr
			}
			res, err := s.life.ImportJail(ctx, in, xr.Next)
			warnings = res.Warnings
			s.journal.deleteRequest("import/" + key)
			if err != nil {
				if ferr := xr.Fail(); ferr != nil {
					// zfs receive reports its own failure when the file ends early or does not match its checksums: the file is the cause
					err = ferr
				}
				return nil, importFileError(err)
			}
			adopt, err := s.RestoreAdopt(ctx, key, jail, w.image)
			if adopt != nil {
				undos = append(undos, adopt)
			}
			if err != nil {
				return nil, err
			}
			ok = true
			s.emitIn(ctx, proj, "jail-created", "/api/v1/jails/"+name)
			s.kick()
			if req.Start {
				if serr := s.StartJail(ctx, key); serr != nil {
					warnings = append(warnings, "the jail was imported but did not start: "+serr.Error())
				}
			}
			j, err := s.getByKey(ctx, key)
			if err != nil {
				return nil, err
			}
			return api.JailImported{Jail: j, Warnings: warnings}, nil
		},
	})
	if err != nil {
		return api.Operation{}, "", err
	}
	submitted = true
	stagedOK = true
	return op, name, nil
}

// receiveVolume makes one volume of the project from its part of the file and registers it.
func (s *Service) receiveVolume(ctx context.Context, p project, si *storageInfo, jail api.ObjectMeta, v importVolume, r io.Reader) error {
	proj := p.Meta.Name
	if err := s.volKind.p(proj).notTaken(v.dst); err != nil {
		return err
	}
	g, _ := specStorageGrant(p.Spec, v.spec.Storage)
	if err := s.ensureProjectDatasets(ctx, proj, si, g); err != nil {
		return err
	}
	req := lifecycle.VolumeRequest{Storage: si.lifecycle(), Project: proj, Name: v.dst, UUID: state_uuid(), Size: v.spec.Size,
		UID: v.spec.UID, GID: v.spec.GID, Mode: v.mode, Encrypted: v.spec.Encrypted}
	if err := s.life.ReceiveVolume(ctx, req, r); err != nil {
		return err
	}
	meta := api.ObjectMeta{Name: v.dst, Project: proj, Labels: v.labels}
	if _, err := s.volKind.p(proj).create(ctx, meta, v.spec); err != nil {
		_ = s.life.DestroyVolume(ctx, si.lifecycle(), proj, v.dst)
		return err
	}
	return nil
}

// importFileError tells the caller what is wrong with the file itself. A file that is cut off or damaged is the caller's
// problem, not an internal error: it would otherwise reach the operation as an untyped error, which readers see masked.
func importFileError(err error) error {
	switch {
	case errors.Is(err, jailxfer.ErrTruncated), errors.Is(err, jailxfer.ErrCorrupt):
		return invalid("%v: nothing was imported", err)
	}
	return err
}

// stagedImport is an export file that has arrived on the host and waits for its operation.
type stagedImport struct {
	f       *os.File
	path    string
	release func()
}

// discard closes and removes the file and gives its slot back; it is safe to call more than once.
func (st *stagedImport) discard() {
	if st.f != nil {
		_ = st.f.Close()
		_ = os.Remove(st.path)
		st.f = nil
		st.release()
	}
}

// The files of imports wait below the state directory, the disk of the database. At most importSlots files are received at once and a
// file never takes the last importReserve bytes of the disk: a big import is refused with a message, it does not stop the daemon.
const (
	importSlots   = 2
	importReserve = 1 << 30
)

var importSlot = make(chan struct{}, importSlots)

// freeBytes is the space an unprivileged writer can still use on the filesystem of dir.
func freeBytes(dir string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}

func (s *Service) importStagingDir() string { return filepath.Join(s.d.StateDir, "imports") }

// stageImport copies the body of an import to a file below the state directory.
func (s *Service) stageImport(rd io.Reader) (*stagedImport, error) {
	dir := s.importStagingDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, api.Errorf(api.CodeInternal, "cannot keep the file of the import: %v", err)
	}
	select {
	case importSlot <- struct{}{}:
	default:
		return nil, api.Errorf(api.CodeBusy, "%d imports are being received now: try again when one has finished", importSlots)
	}
	release := func() { <-importSlot }
	f, err := os.CreateTemp(dir, "import-*.export")
	if err != nil {
		release()
		return nil, api.Errorf(api.CodeInternal, "cannot keep the file of the import: %v", err)
	}
	st := &stagedImport{f: f, path: f.Name(), release: release}
	// the file may use what is free except the reserve; one byte more than that is how a file that is too big shows
	room := int64(-1)
	if free, err := freeBytes(dir); err == nil {
		if free > importReserve {
			room = int64(free - importReserve)
		} else {
			room = 0
		}
	}
	var src io.Reader = rd
	if room >= 0 {
		src = io.LimitReader(rd, room+1)
	}
	n, err := io.Copy(f, src)
	if err == nil && room >= 0 && n > room {
		err = syscall.ENOSPC
	}
	if err != nil {
		st.discard()
		if errors.Is(err, syscall.ENOSPC) {
			return nil, api.Errorf(api.CodePreconditionFailed, "the file does not fit in %s (a reserve of 1 GiB stays free): free space there or import from a host with more", dir)
		}
		return nil, importFileError(err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		st.discard()
		return nil, api.Errorf(api.CodeInternal, "cannot read the file of the import: %v", err)
	}
	// the slot limits the files that are being received; a file that has arrived waits for its operation without one
	release()
	st.release = func() {}
	return st, nil
}

// cleanImportStaging removes the files of imports that a restart interrupted: the operation that read them is gone.
func (s *Service) cleanImportStaging() {
	dir := s.importStagingDir()
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "import-") {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
