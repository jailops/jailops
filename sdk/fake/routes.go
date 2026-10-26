package fake

import (
	"net/http"
	"slices"
	"strconv"
	"strings"

	"jailops.net/jailops/sdk/api"
)

func (s *Server) routes() {
	s.jails = newKind(s, "jails", "jail", api.ScopeHost, func(j *api.Jail) *api.ObjectMeta { return &j.Metadata })
	s.jails.scoped = true
	s.images = newKind(s, "images", "image", api.ScopeFleet, func(v *api.Image) *api.ObjectMeta { return &v.Metadata })
	s.networks = newKind(s, "networks", "network", api.ScopeFleet, func(v *api.Network) *api.ObjectMeta { return &v.Metadata })
	s.policies = newKind(s, "firewall/policies", "firewall-policy", api.ScopeFleet, func(v *api.FirewallPolicy) *api.ObjectMeta { return &v.Metadata })
	s.addrsets = newKind(s, "firewall/address-sets", "address-set", api.ScopeFleet, func(v *api.AddressSet) *api.ObjectMeta { return &v.Metadata })
	s.targets = newKind(s, "backup-targets", "backup-target", api.ScopeFleet, func(v *api.BackupTarget) *api.ObjectMeta { return &v.Metadata })
	s.bpolicies = newKind(s, "backup-policies", "backup-policy", api.ScopeFleet, func(v *api.BackupPolicy) *api.ObjectMeta { return &v.Metadata })
	s.backups = newKind(s, "backups", "backup", api.ScopeHost, func(v *api.Backup) *api.ObjectMeta { return &v.Metadata })
	s.remotes = newKind(s, "remotes", "remote", api.ScopeFleet, func(v *api.Remote) *api.ObjectMeta { return &v.Metadata })
	s.tokens = newKind(s, "tokens", "token", api.ScopeFleet, func(v *api.Token) *api.ObjectMeta { return &v.Metadata })
	s.certs = newKind(s, "certificates", "certificate", api.ScopeFleet, func(v *api.Certificate) *api.ObjectMeta { return &v.Metadata })
	s.projects = newKind(s, "projects", "project", api.ScopeHost, func(v *api.Project) *api.ObjectMeta { return &v.Metadata })
	s.storages = newKind(s, "storage", "storage", api.ScopeHost, func(v *api.Storage) *api.ObjectMeta { return &v.Metadata })
	s.users = newKind(s, "users", "user", api.ScopeFleet, func(v *api.User) *api.ObjectMeta { return &v.Metadata })

	s.profiles = newKind(s, "profiles", "profile", api.ScopeFleet, func(v *api.Profile) *api.ObjectMeta { return &v.Metadata })
	s.profiles.scoped = true
	s.setupProfiles()
	s.policies.scoped, s.addrsets.scoped, s.bpolicies.scoped = true, true, true
	s.backups.crossProject = true
	s.setupProjects()
	s.setupStorage()
	s.setupAccess()
	s.setupJails()
	s.setupImages()
	s.setupNetworks()
	s.backups.readonly = true
	s.tokens.noCreate = true
	s.tokens.noUpdate = true
	s.users.noCreate = true
	s.targets.validate = func(o, _ *api.BackupTarget) *api.Error {
		if o.Spec.Kind != "node" && o.Spec.Kind != "pool" {
			return api.Errorf(api.CodeInvalid, "backup target kind must be node or pool")
		}
		return nil
	}

	m := muxer(func(pattern string, h http.HandlerFunc) {
		s.patterns = append(s.patterns, pattern)
		s.mux.HandleFunc(pattern, h)
	})
	m("GET /1.0", s.handleInfo)
	m("GET /1.0/events", s.handleEvents)
	m("GET /1.0/operations", s.handleOpList)
	m("GET /1.0/operations/{id}", s.handleOpGet)
	m("GET /1.0/operations/{id}/wait", s.handleOpWait)
	m("DELETE /1.0/operations/{id}", s.handleOpCancel)
	m("POST /1.0/restores", s.handleRestore)
	m("POST /1.0/tokens", s.createToken)
	m("POST /1.0/users", s.createUser)
	m("POST /1.0/users/{name}/password", s.setUserPassword)
	for _, reg := range []func(muxer){
		s.jails.register, s.images.register, s.networks.register, s.policies.register, s.addrsets.register,
		s.targets.register, s.bpolicies.register, s.backups.register, s.remotes.register,
		s.profiles.register, s.tokens.register, s.certs.register, s.users.register, s.projects.register, s.storages.register,
	} {
		reg(m)
	}
	m("POST /1.0/trust/tokens", s.createTrustToken)
	m("POST /1.0/trust/clients", s.redeemTrustToken)
	m("GET /1.0/sessions", s.listSessions)
	m("DELETE /1.0/sessions/{id}", s.revokeSession)
	m("GET /1.0/auth/session", s.handleWhoami)
	m("GET /1.0/config-keys", s.handleConfigKeys)
	m("POST /1.0/jails/{name}/config", s.jailConfig)
	m("POST /1.0/jails/{name}/exec", s.jailExec)
	m("POST /1.0/jails/{name}/console", s.consoleOpen)
	m("GET /1.0/jails/{name}/console/{id}/output", s.consoleOutput)
	m("POST /1.0/jails/{name}/console/{id}/input", s.consoleInput)
	m("POST /1.0/jails/{name}/console/{id}/resize", s.consoleResize)
	m("DELETE /1.0/jails/{name}/console/{id}", s.consoleClose)
	m("GET /1.0/updates", s.handleUpdatesList)
	m("POST /1.0/updates/check", s.handleUpdatesCheck)
	m("POST /1.0/updates/apply", s.handleUpdatesApply)
	m("GET /1.0/jails/{name}/updates", s.handleUpdateHistory)
	m("POST /1.0/jails/{name}/provision", s.handleProvision)
	m("GET /1.0/backup/key", s.handleBackupKeyStatus)
	m("POST /1.0/backup/key", s.handleBackupKeyCreate)
	m("POST /1.0/backup/key/escrow", s.handleBackupKeyEscrow)
	m("POST /1.0/backups/run", s.handleBackupRun)
	m("POST /1.0/backups/{id}/verify", s.handleBackupVerify)
	m("GET /1.0/jails/{name}/state", s.jailState)
	m("PUT /1.0/jails/{name}/state", s.jailAction)
	m("GET /1.0/jails/{name}/snapshots", s.snapList)
	m("POST /1.0/jails/{name}/snapshots", s.snapCreate)
	m("GET /1.0/jails/{name}/snapshots/{snap}", s.snapGet)
	m("DELETE /1.0/jails/{name}/snapshots/{snap}", s.snapDelete)
	m("POST /1.0/jails/{name}/rollback", s.jailRollback)
	m("POST /1.0/jails/{name}/upgrade", s.jailUpgrade)
	m("GET /1.0/images/{name}/generations", s.genList)
	m("POST /1.0/images/{name}/generations", s.genBuild)
	m("DELETE /1.0/images/{name}/generations/{n}", s.genDelete)
	m("PUT /1.0/images/{name}/aliases/{alias}", s.aliasSet)
	m("DELETE /1.0/images/{name}/aliases/{alias}", s.aliasDelete)
	m("GET /1.0/networks/{name}/leases", s.leases)
	m("/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, api.Errorf(api.CodeNotFound, "no such route %s %s", r.Method, r.URL.Path))
	})
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	auth := "trusted"
	if rc, aerr := s.authenticate(r); aerr != nil || !rc.authed {
		auth = "untrusted"
	}
	s.mu.Lock()
	info := api.ServerInfo{
		APIVersion: api.Version, APIExtensions: slices.Clone(s.extensions), Version: "fake",
		HostUUID: s.uuid, Hostname: s.hostname, Role: api.RoleHost, Hub: s.hub, Auth: auth,
		Environment: api.Environment{OS: "FreeBSD", Arch: "amd64", Pool: "tank"}, SecretsRecipient: s.secretsRec,
	}
	s.mu.Unlock()
	writeSync(w, 200, info)
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	t, e := decode[api.Token](r)
	if e != nil {
		writeErr(w, e)
		return
	}
	s.mu.Lock()
	rp := func() reply {
		if t.Spec.Permission == "" {
			return reply{err: api.Errorf(api.CodeInvalid, "token permission is required")}
		}
		if e := validIdentityScope(t.Spec.Projects, t.Spec.Global); e != nil {
			return reply{err: e}
		}
		if e := s.checkProjectsExist(t.Spec.Projects); e != nil {
			return reply{err: e}
		}
		if t.Metadata.Name == "" {
			t.Metadata.Name = "token-" + randHex(4)
		}
		if !validName(t.Metadata.Name) {
			return reply{err: api.Errorf(api.CodeInvalid, "invalid name %q", t.Metadata.Name)}
		}
		if s.tokens.items[t.Metadata.Name] != nil {
			return reply{err: api.Errorf(api.CodeAlreadyExists, "token %q already exists", t.Metadata.Name)}
		}
		t.Secret = ""
		s.tokens.insert(t, false)
		out := clone(t)
		out.Secret = "jot_" + randHex(16) // shown once, never stored
		return reply{status: 201, meta: out}
	}()
	s.mu.Unlock()
	s.respond(w, rp)
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	req, e := decode[api.UserCreate](r)
	if e != nil {
		writeErr(w, e)
		return
	}
	s.mu.Lock()
	rp := func() reply {
		name := req.Metadata.Name
		if !validName(name) {
			return reply{err: api.Errorf(api.CodeInvalid, "invalid name %q", name)}
		}
		if e := validIdentityScope(req.Spec.Projects, req.Spec.Global); e != nil {
			return reply{err: e}
		}
		if s.users.items[name] != nil {
			return reply{err: api.Errorf(api.CodeAlreadyExists, "user %q already exists", name)}
		}
		u := &api.User{Metadata: req.Metadata, Spec: req.Spec}
		s.users.insert(u, false)
		out := api.UserCreated{User: clone(u)}
		if req.Password == "" {
			out.Password = randHex(10)
		}
		return reply{status: 201, meta: out}
	}()
	s.mu.Unlock()
	s.respond(w, rp)
}

// setUserPassword implements POST /1.0/users/{name}/password.
func (s *Server) setUserPassword(w http.ResponseWriter, r *http.Request) {
	req, e := decodeOptional[api.PasswordSet](r)
	if e != nil {
		writeErr(w, e)
		return
	}
	s.mu.Lock()
	rp := func() reply {
		name := r.PathValue("name")
		if s.users.items[name] == nil {
			return reply{err: api.Errorf(api.CodeNotFound, "user %q not found", name)}
		}
		var out api.PasswordSet
		if req.Password == "" {
			out.Password = randHex(10)
		}
		return reply{status: 200, meta: out}
	}()
	s.mu.Unlock()
	s.respond(w, rp)
}

// handleRestore implements POST /1.0/restores.
func (s *Server) handleRestore(w http.ResponseWriter, r *http.Request) {
	req, e := decode[api.RestoreRequest](r)
	if e != nil {
		writeErr(w, e)
		return
	}
	rc := s.info(r)
	s.mu.Lock()
	rp := func() reply {
		if req.Host != "" && req.Host != s.uuid {
			return reply{err: api.Errorf(api.CodeUnsupported, "the fake restores onto itself only")}
		}
		b := s.backups.items[req.Backup]
		if b == nil {
			return reply{err: notFound("backup", req.Backup)}
		}
		name := req.As
		if name == "" {
			name = b.Spec.Jail
		}
		// The jail goes into the project the body names, else the request's.
		if req.Project != "" {
			if s.projects.items[req.Project] == nil {
				return reply{err: notFound("project", req.Project)}
			}
			rc = &reqInfo{id: rc.id, kind: rc.kind, authed: rc.authed, project: req.Project}
		}
		pf := api.RestorePreflight{}
		spec := clone(&b.Spec.Bundle.Spec)
		for i := range spec.Network {
			n := spec.Network[i].Network
			if m, ok := req.NetworkMap[n]; ok {
				n = m
			}
			spec.Network[i].Network = n
			if s.networks.items[n] == nil {
				pf.Missing = append(pf.Missing, "network "+n)
			}
			if !req.KeepIP {
				if spec.Network[i].IPv4 != "" && spec.Network[i].IPv4 != "none" {
					spec.Network[i].IPv4 = "auto"
				}
				if spec.Network[i].IPv6 != "" && spec.Network[i].IPv6 != "none" {
					spec.Network[i].IPv6 = "auto"
				}
			}
		}
		if s.images.items[imageName(spec.Image)] == nil {
			pf.Missing = append(pf.Missing, "image "+imageName(spec.Image))
		}
		if s.jails.items[s.jails.keyIn(rc.project, name)] != nil && !req.Replace {
			pf.Errors = append(pf.Errors, "jail "+name+" already exists")
		} else {
			pf.Changes = append(pf.Changes, "create jail "+name)
		}
		pf.OK = len(pf.Missing) == 0 && len(pf.Errors) == 0
		if req.DryRun {
			return reply{status: 200, meta: pf}
		}
		if !pf.OK {
			return reply{err: api.Errorf(api.CodeInvalid, "restore preflight failed").
				WithDetail("missing", strings.Join(pf.Missing, "; ")).WithDetail("errors", strings.Join(pf.Errors, "; "))}
		}
		jail := &api.Jail{Metadata: api.ObjectMeta{Name: name, Project: rc.project, Labels: nil}, Spec: spec}
		op := s.startOp(rc.project, "restores.create", "Restoring "+name, map[string][]string{"jails": {"/1.0/jails/" + name}}, func() (any, *api.Error) {
			delete(s.jails.items, s.jails.keyIn(rc.project, name))
			if e := s.assignJail(jail, nil); e != nil {
				return nil, e
			}
			s.initJailStatus(jail)
			s.jails.insert(jail, false)
			return clone(jail), nil
		})
		return reply{op: &op}
	}()
	s.mu.Unlock()
	s.respond(w, rp)
}

func imageName(ref string) string {
	if i := strings.IndexAny(ref, "@#"); i >= 0 {
		return ref[:i]
	}
	return ref
}

func genNumber(s string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(s, "g"))
	return n
}
