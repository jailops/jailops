package backupd

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"jailops.net/jailops/internal/auth"
	"jailops.net/jailops/internal/backupui"
	"jailops.net/jailops/internal/node"
	"jailops.net/jailops/internal/node/ctl"
	"jailops.net/jailops/sdk/api"
)

const usage = `jailops-backupd: the JailOps backup node.

usage: jailops-backupd [-c config] <command> [arguments]

Daemon:
  serve [--adopt]                  run the node (host listener, admin socket, management API and UI)
  init [--admin NAME] [--key-domain ID]
                                   create the repository, probe the datastore, migrate the database,
                                   create the server certificate, optionally create the first admin
  version

Administration (through the admin socket of the running daemon; root or the admin group):
  status                           repository status
  gc [--grace 36h] [--dry-run]     garbage collection
  verify [all|new|stale]           keyless verification (default new) [--no-quarantine]
  retention [--dry-run]            apply the retention policies
  maintenance [online|read-only|offline]
  protect GROUP SNAPSHOT | unprotect GROUP SNAPSHOT
  delete request GROUP SNAPSHOT [--delay 72h] | delete cancel GROUP SNAPSHOT | delete execute GROUP SNAPSHOT
  quarantine list
  reconcile                        rebuild the snapshot index from the repository
  jobs [ID]                        recent jobs, or one job with its result

Database (direct):
  hosts enroll NAME (--fingerprint FP | --cert FILE) [--scopes backup-push,backup-pull] [--note TEXT]
  hosts list | hosts revoke NAME
  users add NAME ROLE [--password-file FILE] | users list | users delete NAME

Options: -c FILE (default ` + DefaultConfigPath + `, or $JAILOPS_BACKUPD_CONFIG), --json for raw answers.
`

type cli struct {
	cfgPath string
	json    bool
	in      io.Reader
	out     io.Writer
	err     io.Writer
}

// RunCLI runs one command line and returns the exit status.
func RunCLI(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	c := &cli{in: stdin, out: stdout, err: stderr, cfgPath: DefaultConfigPath}
	if v := os.Getenv("JAILOPS_BACKUPD_CONFIG"); v != "" {
		c.cfgPath = v
	}
	// global options come first
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		switch {
		case args[0] == "-c" && len(args) > 1:
			c.cfgPath, args = args[1], args[2:]
		case strings.HasPrefix(args[0], "-c="):
			c.cfgPath, args = strings.TrimPrefix(args[0], "-c="), args[1:]
		case args[0] == "--json":
			c.json, args = true, args[1:]
		case args[0] == "-h" || args[0] == "--help":
			fmt.Fprint(stdout, usage)
			return 0
		default:
			fmt.Fprintf(stderr, "unknown option %s\n\n%s", args[0], usage)
			return 2
		}
	}
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	cmd, rest := args[0], args[1:]
	var err error
	switch cmd {
	case "version":
		fmt.Fprintf(stdout, "jailops-backupd %s (schema %s)\n", Version, ctl.SchemaVersion())
	case "serve":
		err = c.serve(ctx, rest)
	case "init":
		err = c.init(ctx, rest)
	case "hosts":
		err = c.hosts(ctx, rest)
	case "users":
		err = c.users(ctx, rest)
	case "status", "gc", "verify", "retention", "maintenance", "protect", "unprotect", "delete", "quarantine", "reconcile", "jobs":
		err = c.admin(ctx, cmd, rest)
	case "help":
		fmt.Fprint(stdout, usage)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", cmd, usage)
		return 2
	}
	if err != nil {
		var ae *api.Error
		if errors.As(err, &ae) && ae.Hint != "" {
			fmt.Fprintf(stderr, "jailops-backupd: %s (%s)\n", ae.Message, ae.Hint)
		} else {
			fmt.Fprintf(stderr, "jailops-backupd: %v\n", err)
		}
		return 1
	}
	return 0
}

func (c *cli) loadConfig() (*Config, error) { return LoadConfig(c.cfgPath) }

func (c *cli) flags(name string, args []string, setup func(*flag.FlagSet)) ([]string, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(c.err)
	setup(fs)
	// flags may follow positional arguments: parse in rounds
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func (c *cli) print(v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Fprintln(c.out, string(b))
}

// ---- serve and init -----------------------------------------------------------------------

func (c *cli) serve(ctx context.Context, args []string) error {
	var adopt bool
	if _, err := c.flags("serve", args, func(fs *flag.FlagSet) {
		fs.BoolVar(&adopt, "adopt", false, "take over a repository owned by another node that is gone for good")
	}); err != nil {
		return err
	}
	cfg, err := c.loadConfig()
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(c.err, nil))
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	var ui = backupui.Handler()
	s, err := New(ctx, cfg, Options{Adopt: adopt, Log: log, WebHandler: ui})
	if err != nil {
		return err
	}
	defer s.Close()
	if err := s.Listen(); err != nil {
		return err
	}
	log.Info("jailops-backupd serving", "version", Version, "hosts", s.HostAddr(), "admin_socket", s.AdminSocket(), "web", s.WebAddr(),
		"repository", s.Node.Meta().ID, "server_certificate", s.ServerFingerprint())
	return s.Serve(ctx)
}

func (c *cli) init(ctx context.Context, args []string) error {
	var admin, pwFile, keyDomain string
	if _, err := c.flags("init", args, func(fs *flag.FlagSet) {
		fs.StringVar(&keyDomain, "key-domain", "", "the 16 hex digit key domain id of the hosts' master key; hosts with another key are refused")
		fs.StringVar(&admin, "admin", "", "create this administrator (the password comes from --password-file or stdin)")
		fs.StringVar(&pwFile, "password-file", "", "file holding the administrator's password")
	}); err != nil {
		return err
	}
	cfg, err := c.loadConfig()
	if err != nil {
		return err
	}
	be, err := cfg.OpenBackend()
	if err != nil {
		return err
	}
	if err := ProbeBackend(ctx, be); err != nil {
		return fmt.Errorf("the datastore is not usable for a repository: %w", err)
	}
	fmt.Fprintln(c.out, "datastore probe: ok")
	db, err := cfg.OpenDB(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	fmt.Fprintf(c.out, "database: ok (schema %s)\n", ctl.SchemaVersion())
	sid := cfg.ServerID
	if sid == "" {
		var existing string
		if err := db.Setting(ctx, "server_id", &existing); err == nil && existing != "" {
			sid = existing
		} else {
			cand, err := newServerID()
			if err != nil {
				return err
			}
			sid = cand
			if err := db.SetSetting(ctx, "server_id", sid); err != nil {
				return err
			}
		}
	}
	meta, err := node.Init(ctx, be, node.InitOptions{ServerID: sid, Host: cfg.Hostname, KeyDomain: keyDomain})
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "repository %s created (owner %s)\n", meta.ID, sid)
	fp, created, err := cfg.EnsureServerCert()
	if err != nil {
		return err
	}
	if created {
		fmt.Fprintf(c.out, "server certificate created: %s\n", cfg.TLS.CertFile)
	}
	fmt.Fprintf(c.out, "server certificate fingerprint (hosts pin this): %s\n", fp)
	if err := cfg.SyncStaticHosts(ctx, db); err != nil {
		return err
	}
	if admin != "" {
		pw, err := c.readPassword(pwFile)
		if err != nil {
			return err
		}
		if err := CreateUser(ctx, db, admin, pw, RoleAdmin, auth.DefaultPasswordParams); err != nil {
			return err
		}
		fmt.Fprintf(c.out, "administrator %s created\n", admin)
	}
	return nil
}

func newServerID() (string, error) {
	var r [8]byte
	if _, err := io.ReadFull(randReader, r[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("node-%x", r), nil
}

func (c *cli) readPassword(file string) (string, error) {
	var b []byte
	var err error
	if file != "" {
		b, err = ReadSecretFile(file)
	} else {
		b, err = io.ReadAll(io.LimitReader(c.in, 1024))
	}
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(b), "\r\n"), nil
}

// ---- hosts and users ---------------------------------------------------------------------------

func (c *cli) hosts(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("hosts: enroll, list or revoke")
	}
	cfg, err := c.loadConfig()
	if err != nil {
		return err
	}
	db, err := cfg.OpenDB(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	switch args[0] {
	case "enroll":
		var fp, certFile, scopes, note string
		pos, err := c.flags("hosts enroll", args[1:], func(fs *flag.FlagSet) {
			fs.StringVar(&fp, "fingerprint", "", "SHA-256 fingerprint of the host's certificate")
			fs.StringVar(&certFile, "cert", "", "the host's certificate (PEM) instead of the fingerprint")
			fs.StringVar(&scopes, "scopes", "", "comma separated: backup-push, backup-pull, restore (default backup-push,backup-pull)")
			fs.StringVar(&note, "note", "", "free text")
		})
		if err != nil {
			return err
		}
		if len(pos) != 1 {
			return errors.New("hosts enroll NAME (--fingerprint FP | --cert FILE)")
		}
		b := HostBody{Name: pos[0], Fingerprint: fp, Note: note}
		if scopes != "" {
			b.Scopes = strings.Split(scopes, ",")
		}
		if certFile != "" {
			pem, err := os.ReadFile(certFile)
			if err != nil {
				return err
			}
			b.Certificate = string(pem)
		}
		h, err := EnrollHost(ctx, db, b)
		if err != nil {
			return err
		}
		_ = db.Audit(ctx, "cli", "host.enroll", h.Name, map[string]any{"fingerprint": h.Fingerprint, "scopes": h.Scopes}, "")
		if c.json {
			c.print(h)
			return nil
		}
		fmt.Fprintf(c.out, "host %s enrolled; pinned certificate fingerprint %s; scopes %s\n", h.Name, h.Fingerprint, strings.Join(h.Scopes, ","))
		if fp, _, err := cfg.EnsureServerCert(); err == nil {
			fmt.Fprintf(c.out, "the host pins this node's certificate: %s\n", fp)
		}
	case "list":
		hs, err := db.Hosts(ctx)
		if err != nil {
			return err
		}
		if c.json {
			c.print(hs)
			return nil
		}
		tw := tabwriter.NewWriter(c.out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tFINGERPRINT\tSCOPES\tSTATE\tLAST SEEN")
		for _, h := range hs {
			state, seen := "active", "never"
			if h.RevokedAt != nil {
				state = "revoked"
			}
			if h.LastSeenAt != nil {
				seen = h.LastSeenAt.Format(time.RFC3339)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", h.Name, h.Fingerprint, strings.Join(h.Scopes, ","), state, seen)
		}
		return tw.Flush()
	case "revoke":
		if len(args) != 2 {
			return errors.New("hosts revoke NAME")
		}
		h, err := db.RevokeHost(ctx, args[1])
		if err != nil {
			return notFoundOr(err, "no such host")
		}
		_ = db.Audit(ctx, "cli", "host.revoke", h.Name, map[string]any{"fingerprint": h.Fingerprint}, "")
		fmt.Fprintf(c.out, "host %s revoked; its open sessions are aborted and the daemon stops accepting it within seconds\n", h.Name)
	default:
		return fmt.Errorf("hosts: unknown subcommand %q", args[0])
	}
	return nil
}

func (c *cli) users(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("users: add, list or delete")
	}
	cfg, err := c.loadConfig()
	if err != nil {
		return err
	}
	db, err := cfg.OpenDB(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	switch args[0] {
	case "add":
		var pwFile string
		pos, err := c.flags("users add", args[1:], func(fs *flag.FlagSet) {
			fs.StringVar(&pwFile, "password-file", "", "file holding the password (default: stdin)")
		})
		if err != nil {
			return err
		}
		if len(pos) != 2 {
			return errors.New("users add NAME ROLE (readonly, backup-admin or admin)")
		}
		pw, err := c.readPassword(pwFile)
		if err != nil {
			return err
		}
		if err := CreateUser(ctx, db, pos[0], pw, pos[1], auth.DefaultPasswordParams); err != nil {
			return err
		}
		_ = db.Audit(ctx, "cli", "user.create", pos[0], map[string]any{"role": pos[1]}, "")
		fmt.Fprintf(c.out, "user %s created\n", pos[0])
	case "list":
		us, err := db.Users(ctx)
		if err != nil {
			return err
		}
		if c.json {
			c.print(us)
			return nil
		}
		tw := tabwriter.NewWriter(c.out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tROLE\tDISABLED")
		for _, u := range us {
			fmt.Fprintf(tw, "%s\t%s\t%v\n", u.Name, u.Role, u.Disabled)
		}
		return tw.Flush()
	case "delete":
		if len(args) != 2 {
			return errors.New("users delete NAME")
		}
		if err := db.DeleteUser(ctx, args[1]); err != nil {
			return notFoundOr(err, "no such user")
		}
		_ = db.Audit(ctx, "cli", "user.delete", args[1], nil, "")
		fmt.Fprintf(c.out, "user %s deleted\n", args[1])
	default:
		return fmt.Errorf("users: unknown subcommand %q", args[0])
	}
	return nil
}

// ---- administration through the socket --------------------------------------------------------

func (c *cli) admin(ctx context.Context, cmd string, args []string) error {
	cfg, err := c.loadConfig()
	if err != nil {
		return err
	}
	ac := NewAdminClient(cfg.AdminSocket)
	base := node.Base + "/admin"
	switch cmd {
	case "status":
		var st node.RepositoryStatus
		if err := ac.Call(ctx, "GET", base+"/status", nil, &st); err != nil {
			return err
		}
		if c.json {
			c.print(st)
			return nil
		}
		fmt.Fprintf(c.out, "repository %s (%s), mode %s\nsnapshots %d in %d groups (%d failed, %d protected, %d pending deletion)\nchunks %d, %d bytes\nopen sessions %d, quarantined %d\nlast GC %s, last verify %s\n",
			st.ID, st.Backend, st.Mode, st.Snapshots, st.Groups, st.Failed, st.Protected, st.PendingDeletion, st.Chunks, st.Bytes, st.OpenSessions, st.Quarantined, since(st.LastGC), since(st.LastVerify))
	case "gc":
		var grace string
		var dry bool
		if _, err := c.flags("gc", args, func(fs *flag.FlagSet) {
			fs.StringVar(&grace, "grace", "", "grace period (default from the configuration, minimum 12h)")
			fs.BoolVar(&dry, "dry-run", false, "report what would be removed")
		}); err != nil {
			return err
		}
		return c.job(ctx, ac, "POST", base+"/gc?wait=1", node.GCRequest{Grace: grace, DryRun: dry})
	case "verify":
		mode, noq := node.VerifyNew, false
		pos, err := c.flags("verify", args, func(fs *flag.FlagSet) {
			fs.BoolVar(&noq, "no-quarantine", false, "report corrupt chunks without moving them aside")
		})
		if err != nil {
			return err
		}
		if len(pos) > 1 {
			return errors.New("verify [all|new|stale]")
		}
		if len(pos) == 1 {
			mode = pos[0]
		}
		return c.job(ctx, ac, "POST", base+"/verify?wait=1", node.VerifyOptions{Mode: mode, Quarantine: !noq})
	case "retention":
		var dry bool
		if _, err := c.flags("retention", args, func(fs *flag.FlagSet) { fs.BoolVar(&dry, "dry-run", false, "show the plan, delete nothing") }); err != nil {
			return err
		}
		return c.job(ctx, ac, "POST", base+"/retention?wait=1", node.RetentionOptions{DryRun: dry})
	case "reconcile":
		return c.job(ctx, ac, "POST", base+"/reconcile?wait=1", nil)
	case "maintenance":
		if len(args) == 0 {
			var m node.ModeBody
			if err := ac.Call(ctx, "GET", base+"/maintenance", nil, &m); err != nil {
				return err
			}
			fmt.Fprintln(c.out, m.Mode)
			return nil
		}
		var m node.ModeBody
		if err := ac.Call(ctx, "PUT", base+"/maintenance", node.ModeBody{Mode: args[0]}, &m); err != nil {
			return err
		}
		fmt.Fprintf(c.out, "maintenance mode: %s\n", m.Mode)
	case "protect", "unprotect":
		if len(args) != 2 {
			return fmt.Errorf("%s GROUP SNAPSHOT", cmd)
		}
		if err := ac.Call(ctx, "PUT", snapPath(base, args[0], args[1])+"/protected", node.ProtectBody{Protected: cmd == "protect"}, nil); err != nil {
			return err
		}
		fmt.Fprintf(c.out, "%sed %s/%s\n", cmd, args[0], args[1])
	case "delete":
		return c.deleteCmd(ctx, ac, base, args)
	case "quarantine":
		if len(args) != 1 || args[0] != "list" {
			return errors.New("quarantine list")
		}
		var q []node.QuarantinedChunk
		if err := ac.Call(ctx, "GET", base+"/quarantine", nil, &q); err != nil {
			return err
		}
		if c.json {
			c.print(q)
			return nil
		}
		tw := tabwriter.NewWriter(c.out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "CHUNK\tSIZE\tSINCE\tHEALED")
		for _, x := range q {
			fmt.Fprintf(tw, "%s\t%d\t%s\t%v\n", x.ID, x.Size, x.Since.Format(time.RFC3339), x.Healed)
		}
		return tw.Flush()
	case "jobs":
		if len(args) == 1 {
			var j ctl.Job
			if err := ac.Call(ctx, "GET", base+"/jobs/"+args[0], nil, &j); err != nil {
				return err
			}
			c.print(j)
			return nil
		}
		var js []ctl.Job
		if err := ac.Call(ctx, "GET", base+"/jobs", nil, &js); err != nil {
			return err
		}
		if c.json {
			c.print(js)
			return nil
		}
		tw := tabwriter.NewWriter(c.out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tKIND\tSTATE\tSTARTED\tBY")
		for _, j := range js {
			st := ""
			if j.StartedAt != nil {
				st = j.StartedAt.Format(time.RFC3339)
			}
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\n", j.ID, j.Kind, j.State, st, j.CreatedBy)
		}
		return tw.Flush()
	}
	return nil
}

func snapPath(base, group, snap string) string {
	return base + "/groups/" + group + "/snapshots/" + snap
}

func (c *cli) deleteCmd(ctx context.Context, ac *AdminClient, base string, args []string) error {
	if len(args) < 3 {
		return errors.New("delete request|cancel|execute GROUP SNAPSHOT")
	}
	sub, group, snap := args[0], args[1], args[2]
	p := snapPath(base, group, snap) + "/deletion"
	switch sub {
	case "request":
		var delay string
		if _, err := c.flags("delete request", args[3:], func(fs *flag.FlagSet) {
			fs.StringVar(&delay, "delay", "", "wait before it may be executed (default from the configuration)")
		}); err != nil {
			return err
		}
		var d node.Deletion
		if err := ac.Call(ctx, "POST", p, node.DeletionBody{Delay: delay}, &d); err != nil {
			return err
		}
		fmt.Fprintf(c.out, "deletion of %s/%s requested; it can be executed after %s\n", group, snap, d.After.Format(time.RFC3339))
	case "cancel":
		if err := ac.Call(ctx, "DELETE", p, nil, nil); err != nil {
			return err
		}
		fmt.Fprintln(c.out, "deletion request cancelled")
	case "execute":
		if err := ac.Call(ctx, "POST", p+"/execute", nil, nil); err != nil {
			return err
		}
		fmt.Fprintf(c.out, "snapshot %s/%s deleted; its chunks are freed by the next garbage collection\n", group, snap)
	default:
		return fmt.Errorf("delete: unknown subcommand %q", sub)
	}
	return nil
}

// job starts a job with ?wait=1 and prints its result.
func (c *cli) job(ctx context.Context, ac *AdminClient, method, path string, body any) error {
	var j ctl.Job
	if err := ac.Call(ctx, method, path, body, &j); err != nil {
		return err
	}
	if c.json {
		c.print(j)
	} else {
		fmt.Fprintf(c.out, "job %d (%s): %s\n", j.ID, j.Kind, j.State)
		if len(j.Result) > 0 {
			var pretty any
			if json.Unmarshal(j.Result, &pretty) == nil {
				b, _ := json.MarshalIndent(pretty, "", "  ")
				fmt.Fprintln(c.out, string(b))
			}
		}
		if j.Error != "" {
			fmt.Fprintf(c.out, "error: %s\n", j.Error)
		}
	}
	if j.State == ctl.JobFailed {
		return errors.New(j.Error)
	}
	return nil
}

func since(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return t.Format(time.RFC3339)
}
