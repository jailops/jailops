package fake

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"

	"jailops.net/jailops/sdk/api"
)

// appEntry is one definition of the fake's catalog.
type appEntry struct {
	detail api.AppDetail
	err    string // the reason when the definition is invalid
}

// seedApps fills the catalog every fake starts with: six valid definitions,
// shortened from the catalog (the reference copy is internal/apps/testdata/catalog).
func (s *Server) seedApps() {
	s.apps = map[string]*appEntry{}
	t, f := true, false
	for _, a := range []api.App{
		{Version: 1, Name: "nginx", Title: "nginx", Summary: "Web server and reverse proxy, serving /usr/local/www.",
			Description: "nginx is a small, fast web server and reverse proxy.", Category: api.AppWeb, Homepage: "https://nginx.org/", License: "BSD-2-Clause",
			Image:     api.AppImage{OS: "freebsd", ReleaseMin: "15.1"},
			Packages:  []api.AppPackage{{Name: "nginx", Repo: "ports", Version: "1.30.6,3"}},
			Services:  []api.AppService{{Name: "nginx", Start: &t}},
			Ports:     []api.AppPort{{Name: "http", Port: 80, Proto: "tcp", Description: "HTTP, the default site.", Expose: true}},
			Data:      []api.AppPath{{Path: "/usr/local/www", Description: "The sites."}},
			Config:    []api.AppConfig{{Path: "/usr/local/etc/nginx/nginx.conf", Description: "The server configuration.", Restart: "nginx"}},
			Logs:      []api.AppLog{{Name: "access", Path: "/var/log/nginx/access.log", Description: "One line per request."}, {Name: "error", Path: "/var/log/nginx/error.log"}},
			Health:    &api.AppHealth{HTTP: &api.AppHTTPCheck{Port: 80, Path: "/", Status: 200}, WaitSeconds: 30},
			Resources: api.AppResources{Memory: "256M", Disk: "2G", CPU: "1"},
			Notes:     "Put the site under /usr/local/www."},
		{Version: 1, Name: "postgresql", Title: "PostgreSQL", Summary: "Relational database server, PostgreSQL 18 from the FreeBSD packages.",
			Description: "PostgreSQL is a relational database with transactions, replication and extensions.\n\nThe cluster listens on localhost only until changed.",
			Category:    api.AppDatabase, Homepage: "https://www.postgresql.org/", License: "PostgreSQL",
			Image:     api.AppImage{OS: "freebsd", ReleaseMin: "15.1"},
			Packages:  []api.AppPackage{{Name: "postgresql18-server", Repo: "ports", Version: "18.6"}},
			Services:  []api.AppService{{Name: "postgresql", Start: &t}},
			Ports:     []api.AppPort{{Name: "postgres", Port: 5432, Proto: "tcp", Description: "The SQL port."}},
			Data:      []api.AppPath{{Path: "/var/db/postgres", Description: "The cluster (data18)."}},
			Config:    []api.AppConfig{{Path: "/var/db/postgres/data18/postgresql.conf", Description: "Server settings.", Restart: "postgresql"}, {Path: "/var/db/postgres/data18/pg_hba.conf", Description: "Who may connect.", Restart: "postgresql"}},
			Logs:      []api.AppLog{{Name: "server", Path: "/var/log/messages", Description: "The port logs to syslog."}},
			Setup:     [][]string{{"/usr/sbin/chown", "postgres:postgres", "/var/db/postgres"}, {"/usr/sbin/service", "postgresql", "initdb"}},
			SysVIPC:   true,
			Health:    &api.AppHealth{Exec: []string{"/usr/local/bin/pg_isready", "-U", "postgres"}, WaitSeconds: 60},
			Resources: api.AppResources{Memory: "1G", Disk: "10G", CPU: "1"},
			Notes:     "Connect from inside the jail with: jailops exec NAME -- su -l postgres -c psql"},
		{Version: 1, Name: "valkey", Title: "Valkey", Summary: "In-memory key-value store under the BSD license.",
			Description: "Valkey serves key/value workloads.", Category: api.AppDatabase, Homepage: "https://valkey.io/", License: "BSD-3-Clause",
			Image:     api.AppImage{OS: "freebsd", ReleaseMin: "15.1"},
			Packages:  []api.AppPackage{{Name: "valkey", Repo: "ports", Version: "9.1.2"}},
			Services:  []api.AppService{{Name: "valkey", Start: &t}},
			Ports:     []api.AppPort{{Name: "valkey", Port: 6379, Proto: "tcp", Description: "The protocol port."}},
			Data:      []api.AppPath{{Path: "/var/db/valkey", Description: "Snapshots and the append-only file."}},
			Config:    []api.AppConfig{{Path: "/usr/local/etc/valkey.conf", Restart: "valkey"}},
			Logs:      []api.AppLog{{Name: "server", Path: "/var/log/valkey/valkey.log"}},
			Setup:     [][]string{{"/bin/mkdir", "-p", "/var/log/valkey", "/var/run/valkey"}, {"/usr/sbin/chown", "valkey:valkey", "/var/db/valkey", "/var/log/valkey", "/var/run/valkey"}},
			Health:    &api.AppHealth{Exec: []string{"/usr/local/bin/valkey-cli", "ping"}, WaitSeconds: 30},
			Resources: api.AppResources{Memory: "512M", Disk: "2G", CPU: "1"},
			Notes:     "Talk to it from inside the jail with: jailops exec NAME -- valkey-cli"},
		{Version: 1, Name: "isms", Title: "isms", Summary: "Information security management platform with git-native documents; needs PostgreSQL.",
			Description: "isms keeps policies, controls and evidence as git-native documents.\n\nThe installation does not configure or start it.",
			Category:    api.AppSecurity, Homepage: "https://github.com/unidoc/isms", License: "Apache-2.0", Icon: "isms.svg",
			Image:     api.AppImage{OS: "freebsd", ReleaseMin: "15.1"},
			Packages:  []api.AppPackage{{Name: "isms", Repo: "jailops", Version: "0.8.1"}, {Name: "openssh-portable", Repo: "ports"}},
			Services:  []api.AppService{{Name: "isms", Start: &f}},
			Ports:     []api.AppPort{{Name: "http", Port: 8080, Proto: "tcp", Description: "The web interface and the API.", Expose: true}},
			Data:      []api.AppPath{{Path: "/var/db/isms", Description: "Git repositories, blobs and the signing key."}},
			Config:    []api.AppConfig{{Path: "/usr/local/etc/isms/server.env", Description: "KEY=VALUE settings; mode 0600, owner isms.", Restart: "isms"}},
			Logs:      []api.AppLog{{Name: "server", Path: "/var/log/isms.log"}},
			Setup:     [][]string{{"/usr/sbin/chown", "isms:isms", "/var/db/isms", "/usr/local/etc/isms/server.env"}},
			Resources: api.AppResources{Memory: "1G", Disk: "10G", CPU: "1"},
			Notes:     "Create the database in a postgresql jail, fill in server.env, run the migrations, then service isms start."},
		{Version: 1, Name: "nsd", Title: "NSD", Summary: "Authoritative DNS server for your own zones, as a primary or a secondary.",
			Description: "NSD answers for the zones it is given and does no recursion.", Category: api.AppNetwork,
			Homepage: "https://nlnetlabs.nl/projects/nsd/about/", License: "BSD-3-Clause", Icon: "nsd.svg",
			Image:    api.AppImage{OS: "freebsd", ReleaseMin: "15.1"},
			Packages: []api.AppPackage{{Name: "nsd", Repo: "ports", Version: "4.15"}},
			Services: []api.AppService{{Name: "nsd", Start: &t}},
			Ports: []api.AppPort{{Name: "dns", Port: 53, Proto: "udp", Description: "DNS queries over UDP.", Expose: true},
				{Name: "dns-tcp", Port: 53, Proto: "tcp", Description: "DNS over TCP and zone transfers.", Expose: true}},
			Data:      []api.AppPath{{Path: "/var/db/nsd", Description: "Zone files and transfer state."}},
			Config:    []api.AppConfig{{Path: "/usr/local/etc/nsd/nsd.conf", Restart: "nsd"}},
			Logs:      []api.AppLog{{Name: "daemon", Path: "/var/log/daemon.log"}},
			Setup:     [][]string{{"/usr/sbin/chown", "-R", "nsd:nsd", "/var/db/nsd"}, {"/usr/local/sbin/nsd-checkconf", "/usr/local/etc/nsd/nsd.conf"}},
			Health:    &api.AppHealth{TCP: 53, WaitSeconds: 30},
			Resources: api.AppResources{Memory: "256M", Disk: "2G", CPU: "1"},
			Notes:     "Put your zones under /var/db/nsd/zones and name them in nsd.conf."},
		{Version: 1, Name: "mail", Title: "Mail (Postfix and Dovecot)", Summary: "A starting point for a single-domain mail server, SMTP with Postfix and IMAP with Dovecot.",
			Description: "Postfix receives mail for one domain; Dovecot serves the mailboxes over IMAP.", Category: api.AppCommunication,
			Homepage: "https://www.postfix.org/", License: "EPL-2.0 OR IPL-1.0 (Postfix), MIT and LGPL-2.1 (Dovecot)", Icon: "mail.svg",
			Image:    api.AppImage{OS: "freebsd", ReleaseMin: "15.1"},
			Packages: []api.AppPackage{{Name: "postfix", Repo: "ports", Version: "3.10"}, {Name: "dovecot", Repo: "ports", Version: "2.3"}},
			Services: []api.AppService{{Name: "postfix", RC: map[string]string{"sendmail_enable": "NONE"}, Start: &t}, {Name: "dovecot", Start: &t}},
			Ports: []api.AppPort{{Name: "smtp", Port: 25, Proto: "tcp", Description: "Mail from other servers.", Expose: true},
				{Name: "submission", Port: 587, Proto: "tcp", Description: "Mail from your users, with TLS.", Expose: true},
				{Name: "imap", Port: 143, Proto: "tcp", Description: "IMAP.", Expose: true}},
			Data:      []api.AppPath{{Path: "/home/mail", Description: "The mail users' homes."}, {Path: "/var/spool/postfix", Description: "The Postfix queue."}},
			Config:    []api.AppConfig{{Path: "/usr/local/etc/postfix/main.cf", Restart: "postfix"}, {Path: "/usr/local/etc/dovecot/dovecot.conf", Restart: "dovecot"}},
			Logs:      []api.AppLog{{Name: "maillog", Path: "/var/log/maillog"}},
			Setup:     [][]string{{"/usr/local/sbin/postfix", "set-permissions"}, {"/usr/local/sbin/postfix", "check"}},
			Health:    &api.AppHealth{TCP: 25, WaitSeconds: 60},
			Resources: api.AppResources{Memory: "512M", Disk: "20G", CPU: "1"},
			Notes:     "A starting point: set the domain, add users, add TLS and the DNS records."},
	} {
		s.SeedApp(a)
	}
}

// SeedApp puts a valid definition into the catalog, replacing one of the
// same name. The hash is derived from the definition.
func (s *Server) SeedApp(a api.App) {
	raw, _ := json.Marshal(a)
	sum := sha256.Sum256(raw)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.apps[a.Name] = &appEntry{detail: api.AppDetail{Definition: a, Hash: "sha256:" + hex.EncodeToString(sum[:]), File: a.Name + ".yaml"}}
}

// SeedInvalidApp puts an invalid definition into the catalog: listed with
// the reason, never served whole.
func (s *Server) SeedInvalidApp(name, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.apps[name] = &appEntry{err: reason, detail: api.AppDetail{File: name + ".yaml"}}
}

// ClearApps empties the catalog.
func (s *Server) ClearApps() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.apps = map[string]*appEntry{}
}

func (s *Server) handleAppsList(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	names := make([]string, 0, len(s.apps))
	for n := range s.apps {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]api.AppSummary, 0, len(names))
	for _, n := range names {
		e := s.apps[n]
		sum := api.AppSummary{Name: n, Valid: e.err == ""}
		if e.err != "" {
			sum.Error = e.err
		} else {
			d := e.detail.Definition
			sum.Title, sum.Summary, sum.Category = d.Title, d.Summary, d.Category
			if len(d.Packages) > 0 {
				sum.Version = d.Packages[0].Version
			}
		}
		out = append(out, sum)
	}
	s.mu.Unlock()
	writeSync(w, 200, out)
}

func (s *Server) handleAppGet(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	s.mu.Lock()
	e := s.apps[name]
	s.mu.Unlock()
	switch {
	case e == nil:
		writeErr(w, api.Errorf(api.CodeNotFound, "app %q is not in the catalog", name).WithHint("jailops app list shows the catalog"))
	case e.err != "":
		writeErr(w, api.Errorf(api.CodeInvalid, "app %q: the definition %s is invalid: %s", name, e.detail.File, e.err).
			WithHint("fix the file in the catalog directory, or reinstall the jailops-apps package").WithDetail("file", e.detail.File))
	default:
		writeSync(w, 200, e.detail)
	}
}

// handleAppInstall implements POST /api/v1/apps/{name}/install: the jail is
// created with the labels of an installation, started, and snapshotted as
// app-installed, all at once. The checks of the daemon that need no host
// (the definition, the jail name, the image's release, the grants) answer
// the same errors.
func (s *Server) handleAppInstall(w http.ResponseWriter, r *http.Request) {
	req, e := decodeOptional[api.AppInstall](r)
	if e != nil {
		writeErr(w, e)
		return
	}
	name := r.PathValue("name")
	rc := s.info(r)
	s.mu.Lock()
	rp := func() reply {
		entry := s.apps[name]
		switch {
		case entry == nil:
			return reply{err: api.Errorf(api.CodeNotFound, "app %q is not in the catalog", name).WithHint("jailops app list shows the catalog")}
		case entry.err != "":
			return reply{err: api.Errorf(api.CodeInvalid, "app %q: the definition %s is invalid and cannot be installed: %s", name, entry.detail.File, entry.err).
				WithHint("fix the file in the catalog directory, or reinstall the jailops-apps package").WithDetail("file", entry.detail.File)}
		}
		app := entry.detail.Definition
		if req.Project != "" && req.Project != rc.project {
			return reply{err: api.Errorf(api.CodeInvalid, "the body names project %q but the request works in %q", req.Project, rc.project)}
		}
		jail := req.Jail
		if jail == "" {
			jail = name
		}
		if !validName(jail) {
			return reply{err: api.Errorf(api.CodeInvalid, "invalid name %q", jail)}
		}
		jk := s.jails.keyIn(rc.project, jail)
		if s.jails.items[jk] != nil {
			return reply{err: api.Errorf(api.CodeAlreadyExists, "jail %q already exists", jail).WithHint("name the jail with --name, or delete the jail first")}
		}
		proj := s.projects.items[rc.project]
		if proj == nil {
			return reply{err: notFound("project", rc.project)}
		}
		network, storage := req.Network, req.Storage
		if network == "" && len(proj.Spec.Networks) == 1 {
			network = proj.Spec.Networks[0].Network
		}
		if storage == "" && len(proj.Spec.Storage) == 1 && proj.Spec.Storage[0].Storage != "*" {
			storage = proj.Spec.Storage[0].Storage
		}
		if network == "" {
			return reply{err: api.Errorf(api.CodeInvalid, "name the network the jail is attached to (--network NAME)")}
		}
		if storage == "" {
			return reply{err: api.Errorf(api.CodeInvalid, "name the storage the jail is created on (--storage NAME)")}
		}
		if !slices.ContainsFunc(proj.Spec.Networks, func(n api.ProjectNetwork) bool { return n.Network == network }) {
			return reply{err: api.Errorf(api.CodeForbidden, "network %q is not granted to project %q", network, rc.project).
				WithHint("an administrator grants networks with `jailops project set`")}
		}
		if !slices.ContainsFunc(proj.Spec.Storage, func(g api.ProjectStorage) bool { return g.Storage == storage || g.Storage == "*" }) {
			return reply{err: api.Errorf(api.CodeForbidden, "storage %q is not granted to project %q", storage, rc.project)}
		}
		image := req.Image
		if image == "" {
			// the only FreeBSD image with the alias stable, as the server does
			var all, found []string
			for n, im := range s.images.items {
				all = append(all, n)
				if im.Spec.OS == api.OSFreeBSD && slices.Contains(im.Spec.Aliases, "stable") {
					found = append(found, n)
				}
			}
			if len(found) != 1 {
				slices.Sort(all)
				slices.Sort(found)
				example := "freebsd-15@stable"
				if len(found) > 1 {
					example = found[0] + "@stable"
				}
				yours := "you have no images"
				if len(all) > 0 {
					yours = "your images: " + strings.Join(all, ", ")
				}
				msg := "no image named, and no FreeBSD image has the alias stable to default to"
				if len(found) > 1 {
					msg = fmt.Sprintf("no image named, and %d FreeBSD images have the alias stable (%s), so there is no default", len(found), strings.Join(found, ", "))
				}
				return reply{err: api.Errorf(api.CodeInvalid, "%s", msg).WithHint("name an image with --image, for example " + example + "; " + yours)}
			}
			image = found[0] + "@stable"
		}
		im := s.images.items[imageName(image)]
		if im == nil {
			return reply{err: notFound("image", imageName(image)).WithHint("create or import the image first")}
		}
		if !releaseAtLeast(im.Spec.Release, app.Image.ReleaseMin) {
			return reply{err: api.Errorf(api.CodeInvalid, "image %q is release %s; app %s needs %s or later", imageName(image), im.Spec.Release, name, app.Image.ReleaseMin).
				WithHint("create an image of a newer release and name it with --image")}
		}
		limits := api.Resources{Memory: app.Resources.Memory, Disk: app.Resources.Disk}
		if c, err := strconv.ParseFloat(app.Resources.CPU, 64); err == nil && c > 0 {
			limits.CPU = int(math.Ceil(c))
		}
		if req.Limits != nil {
			limits = *req.Limits
		}
		var vols []api.VolumeMount
		for _, d := range app.Data {
			vols = append(vols, api.VolumeMount{Name: volumeName(d.Path), Mount: d.Path})
		}
		j := &api.Jail{Metadata: api.ObjectMeta{Name: jail, Project: rc.project, Labels: map[string]string{api.LabelApp: name, api.LabelAppHash: entry.detail.Hash}},
			Spec: api.JailSpec{Image: image, OS: api.OSFreeBSD, Storage: storage, Hostname: jail, Layout: api.LayoutThin,
				Network: []api.NetworkAttach{{Network: network}}, Volumes: vols, Resources: limits, SysVIPC: app.SysVIPC}}
		steps := []string{"01 create-jail: create thin jail " + jail + " from " + image, "02 start-jail: start " + jail}
		n := 3
		step := func(name, what string) {
			steps = append(steps, fmt.Sprintf("%02d %s: %s", n, name, what))
			n++
		}
		// one step per repository, the FreeBSD one first, as the daemon does
		var ports, jailops []string
		for _, p := range app.Packages {
			if p.Repo == "jailops" {
				jailops = append(jailops, p.Name)
			} else {
				ports = append(ports, p.Name)
			}
		}
		if len(ports) > 0 {
			step("install-packages-ports", "pkg -r <root> install -y "+strings.Join(ports, " "))
		}
		if len(jailops) > 0 {
			step("install-packages-jailops", "pkg -r <root> install -y -r jailops "+strings.Join(jailops, " "))
		}
		step("record-version", "pkg -r <root> query %v "+app.Packages[0].Name+", then label "+api.LabelAppVersion)
		if len(app.Services) > 0 {
			step("write-rc-conf", "set the services' rc.conf variables in <root>/etc/rc.conf")
		}
		for i, argv := range app.Setup {
			step("setup-"+strconv.Itoa(i+1), "in the jail: "+strings.Join(argv, " "))
		}
		for _, svc := range app.Services {
			if svc.Start == nil || *svc.Start {
				step("start-service-"+svc.Name, "in the jail: /usr/sbin/service "+svc.Name+" start")
			}
		}
		if app.Health != nil {
			step("health-check", "the health check of the definition")
		}
		step("snapshot", "snapshot every dataset of "+jail+" as app-installed")
		res := api.AppInstallResult{App: name, Hash: entry.detail.Hash, Data: app.Data, Config: app.Config, Logs: app.Logs, Notes: app.Notes, Steps: steps, DryRun: req.DryRun}
		desc := "Install app " + name + " as jail " + jail
		if req.DryRun {
			desc += " (dry run)"
		}
		op := s.startOp(rc.project, "apps.install", desc, map[string][]string{"jails": {"/api/v1/jails/" + jail}}, func() (any, *api.Error) {
			if req.DryRun {
				return res, nil
			}
			if s.jails.items[jk] != nil {
				return nil, api.Errorf(api.CodeAlreadyExists, "jail %q already exists", jail)
			}
			if e := s.assignJail(j, nil); e != nil {
				return nil, e
			}
			s.initJailStatus(j)
			s.jails.insert(j, false)
			s.nextJID++
			j.Status.Power, j.Status.JID, j.Status.StartedAt = api.PowerRunning, s.nextJID, s.now()
			for _, a := range j.Spec.Network {
				for _, ad := range []string{a.IPv4, a.IPv6} {
					if ad != "" && ad != "none" && ad != "auto" {
						j.Status.Addresses = append(j.Status.Addresses, ad)
					}
				}
			}
			s.emitIn(rc.project, "jail-started", "/api/v1/jails/"+jail)
			if v := app.Packages[0].Version; v != "" {
				j.Metadata.Labels[api.LabelAppVersion] = v
				res.Version = v
			}
			j.Metadata.Generation++
			sn := &api.JailSnapshot{
				Metadata: api.ObjectMeta{Name: "app-installed", UUID: newUUID(), Project: rc.project, Scope: api.ScopeHost, Host: s.uuid, Generation: 1, CreatedAt: s.now(), UpdatedAt: s.now()},
				Spec:     api.JailSnapshotSpec{Jail: jail, Comment: "app " + name + " as installed"},
			}
			s.snapshots[jk] = append(s.snapshots[jk], sn)
			res.Jail, res.Snapshot, res.Addresses = clone(j), sn.Metadata.Name, slices.Clone(j.Status.Addresses)
			for _, pt := range app.Ports {
				ip := api.AppInstalledPort{Name: pt.Name, Port: pt.Port, Proto: pt.Proto, Expose: pt.Expose, Description: pt.Description}
				if len(j.Status.Addresses) > 0 {
					ip.Endpoint = net.JoinHostPort(j.Status.Addresses[0], strconv.Itoa(pt.Port))
				}
				res.Ports = append(res.Ports, ip)
			}
			s.emitIn(rc.project, "app.installed", "/api/v1/jails/"+jail)
			return res, nil
		})
		return reply{op: &op}
	}()
	s.mu.Unlock()
	s.respond(w, rp)
}

// volumeName names the dataset of a data path the way the daemon does.
func volumeName(p string) string {
	var b strings.Builder
	dash := true
	for _, r := range strings.ToLower(strings.Trim(p, "/")) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dash = false
		} else if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	n := strings.TrimRight(b.String(), "-")
	if len(n) > 40 {
		n = strings.TrimRight(n[:40], "-")
	}
	if n == "" {
		n = "data"
	}
	return n
}

// releaseAtLeast compares "15.1-RELEASE" with "15.1".
func releaseAtLeast(release, minimum string) bool {
	num := func(s string) (int, int, bool) {
		s, _, _ = strings.Cut(s, "-")
		a, b, ok := strings.Cut(s, ".")
		if !ok {
			return 0, 0, false
		}
		x, err1 := strconv.Atoi(a)
		y, err2 := strconv.Atoi(b)
		return x, y, err1 == nil && err2 == nil
	}
	ra, rb, ok1 := num(release)
	ma, mb, ok2 := num(minimum)
	return ok1 && ok2 && (ra > ma || ra == ma && rb >= mb)
}
