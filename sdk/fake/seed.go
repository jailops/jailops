package fake

import "jailops.net/jailops/sdk/api"

// The Seed helpers store objects directly, without events and without
// validation, to set up a test. Identity fields (UUID, generation, times) are
// filled when empty. They are the only way to create backups, which the real
// API never creates through a client.

// needProject stops a test that seeds a project-scoped object without saying
// which project it belongs to: the server has no default project.
func needProject(m *api.ObjectMeta, what string) {
	if m.Project == "" {
		panic("fake: " + what + " needs metadata.project: there is no default project")
	}
}

// SeedJail stores a jail. A zero status becomes stopped on generation g1.
// "auto" addresses are kept as written.
func (s *Server) SeedJail(j api.Jail) {
	needProject(&j.Metadata, "SeedJail")
	s.mu.Lock()
	defer s.mu.Unlock()
	if j.Status.Power == "" {
		keep := j.Status
		s.initJailStatus(&j)
		if keep.Generation != "" {
			j.Status.Generation = keep.Generation
		}
	}
	s.jails.insert(&j, true)
}

// SeedImage stores an image.
func (s *Server) SeedImage(i api.Image) { s.mu.Lock(); defer s.mu.Unlock(); s.images.insert(&i, true) }

// SeedGeneration appends the next generation to an image and returns it.
func (s *Server) SeedGeneration(image string) api.ImageGeneration {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.nextGen(image)
	g := &api.ImageGeneration{Number: n, Name: image + "#g" + itoa(n), Manifest: "sha256:" + randHex(16), CreatedAt: s.now()}
	s.gens[image] = append(s.gens[image], g)
	s.genMax[image] = n
	s.followAliases(image, n)
	return *g
}

// SeedImageCurrent makes an upgrade of the image find nothing to upgrade: it
// keeps no generation and moves no jail (current false restores the default).
func (s *Server) SeedImageCurrent(image string, current bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.imgSame[image] = current
}

// SeedNetwork stores a network.
func (s *Server) SeedNetwork(n api.Network) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.networks.insert(&n, true)
}

// SeedFirewallPolicy stores a firewall policy.
func (s *Server) SeedFirewallPolicy(p api.FirewallPolicy) {
	needProject(&p.Metadata, "SeedFirewallPolicy")
	s.mu.Lock()
	defer s.mu.Unlock()
	s.policies.insert(&p, true)
}

// SeedAddressSet stores an address set.
func (s *Server) SeedAddressSet(a api.AddressSet) {
	needProject(&a.Metadata, "SeedAddressSet")
	s.mu.Lock()
	defer s.mu.Unlock()
	s.addrsets.insert(&a, true)
}

// SeedBackupTarget stores a backup target.
func (s *Server) SeedBackupTarget(t api.BackupTarget) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.targets.insert(&t, true)
}

// SeedBackupPolicy stores a backup policy.
func (s *Server) SeedBackupPolicy(p api.BackupPolicy) {
	needProject(&p.Metadata, "SeedBackupPolicy")
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bpolicies.insert(&p, true)
}

// SeedBackup stores a backup.
func (s *Server) SeedBackup(b api.Backup) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.backups.insert(&b, true)
}

// SeedRemote stores a remote.
func (s *Server) SeedRemote(r api.Remote) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.remotes.insert(&r, true)
}

// JailIn returns a copy of a stored jail of a project.
func (s *Server) JailIn(project, name string) (api.Jail, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j := s.jails.items[project+"/"+name]
	if j == nil {
		return api.Jail{}, false
	}
	return clone(j), true
}

// SeedProject stores a project without validation.
func (s *Server) SeedProject(p api.Project) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.projects.insert(&p, true)
}

// SeedStorage stores a storage without validation.
func (s *Server) SeedStorage(st api.Storage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.storages.insert(&st, true)
}

// Project returns a copy of a stored project.
func (s *Server) Project(name string) (api.Project, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.projects.items[name]
	if p == nil {
		return api.Project{}, false
	}
	c := clone(p)
	c.Status = s.projectUsage(name)
	c.Status.Conditions = append(c.Status.Conditions, s.fleetConds[name]...)
	return c, true
}

// Network returns a copy of a stored network.
func (s *Server) Network(name string) (api.Network, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.networks.items[name]
	if n == nil {
		return api.Network{}, false
	}
	return clone(n), true
}

// Snapshots returns the snapshot names of a jail of a project.
func (s *Server) Snapshots(project, jail string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, sn := range s.snapshots[project+"/"+jail] {
		out = append(out, sn.Metadata.Name)
	}
	return out
}

// Touch bumps a jail's generation as if another client had changed it, which
// makes a held ETag stale. Use it to provoke a conflict.
func (s *Server) Touch(project, jail string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	j := s.jails.items[project+"/"+jail]
	if j == nil {
		return false
	}
	j.Metadata.Generation++
	return true
}

// SetSnapshotWritten sets the number of bytes a snapshot reports as written
// since it was taken (what a data rollback would discard). It reports whether
// the snapshot exists.
func (s *Server) SetSnapshotWritten(project, jail, snapshot string, n int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	sn := s.findSnap(project+"/"+jail, snapshot)
	if sn == nil {
		return false
	}
	sn.Status.Written = n
	return true
}
