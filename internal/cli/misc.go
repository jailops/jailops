package cli

import (
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

type check struct {
	Name   string `json:"check"`
	Status string `json:"status"` // PASS, INFO, WARN, FAIL
	Detail string `json:"detail"`
}

func (a *app) doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check the connection and the server",
		Long: `Run a series of checks and print PASS, WARN or FAIL for each (INFO for a fact the
server reports that is not a fault, such as a setting it stores and does not enforce yet): which
configuration is used, whether the server answers, how it sees you, whether the
API versions and extensions match, and what the host reports about ZFS and PF.
The exit status is 1 when a check failed.`,
		Example: "jailops doctor\njailops --remote prod doctor -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			var checks []check
			add := func(n, s, d string) { checks = append(checks, check{n, s, d}) }
			conn, err := a.resolveConnection()
			if err != nil {
				add("configuration", "FAIL", err.Error())
			} else {
				src := []string{}
				for _, k := range sortedKeys(conn.Sources) {
					src = append(src, k+": "+conn.Sources[k])
				}
				name := conn.Name
				if name == "" {
					name = "local"
				}
				add("configuration", "PASS", fmt.Sprintf("remote %s -> %s (%s)", name, conn.describe(), strings.Join(src, "; ")))
				if conn.Host != "" {
					add("host scope", "PASS", "requests go to host "+conn.Host+" through the hub")
				}
				var c *sdk.Client
				if c, err = a.connect(ctx); err != nil {
					add("connection", "FAIL", err.Error())
					if h := transportHint(err); h != "" {
						add("hint", "WARN", h)
					}
				} else {
					info, ierr := c.Info(ctx)
					if ierr != nil {
						add("connection", "FAIL", ierr.Error())
					} else {
						add("connection", "PASS", fmt.Sprintf("%s, server %s, API %s", info.Hostname, info.Version, info.APIVersion))
						if info.Auth == "trusted" {
							add("authentication", "PASS", "the server trusts this client")
						} else {
							add("authentication", "WARN", "the server sees this client as "+info.Auth+": most calls will be refused")
						}
						if info.APIVersion != api.Version {
							add("api version", "WARN", fmt.Sprintf("server speaks %s, this client %s", info.APIVersion, api.Version))
						} else {
							add("api version", "PASS", info.APIVersion)
						}
						var missing []string
						for _, e := range api.Extensions {
							if !slices.Contains(info.APIExtensions, e) {
								missing = append(missing, e)
							}
						}
						if len(missing) > 0 {
							add("extensions", "WARN", "the server lacks: "+strings.Join(missing, ", "))
						} else {
							add("extensions", "PASS", fmt.Sprintf("all %d known extensions present", len(api.Extensions)))
						}
						env := info.Environment
						if env.Pool == "" {
							add("zfs", "WARN", "the server reports no ZFS pool")
						} else {
							detail := "pool " + env.Pool
							if env.ZFSVersion != "" {
								detail += " (zfs " + env.ZFSVersion + ")"
							}
							add("zfs", "PASS", detail)
						}
						if env.PF {
							add("pf", "PASS", "packet filter available")
						} else {
							add("pf", "WARN", "the server reports no packet filter: firewall policies cannot be applied")
						}
						if _, lerr := collect(c.Operations().List(ctx)); lerr != nil {
							add("permissions", "WARN", "cannot list operations: "+lerr.Error())
						} else {
							add("permissions", "PASS", "can read operations")
						}
						if c.Has("doctor") {
							rep, derr := c.Doctor(ctx)
							switch {
							case derr != nil:
								add("server diagnostics", "WARN", "not available: "+derr.Error())
							default:
								for _, sc := range rep.Checks {
									st := map[api.DoctorStatus]string{api.DoctorOK: "PASS", api.DoctorFail: "FAIL", api.DoctorInfo: "INFO"}[sc.Status]
									if st == "" {
										st = "WARN"
									}
									msg := sc.Message
									if sc.Hint != "" && sc.Status != api.DoctorOK {
										msg += " (" + sc.Hint + ")"
									}
									add("server: "+sc.Name, st, msg)
								}
							}
						}
					}
				}
			}
			bad := 0
			for _, c := range checks {
				if c.Status == "FAIL" {
					bad++
				}
			}
			if err := a.emit(checks, stateStyle(1), func() ([]string, [][]string) {
				var rows [][]string
				for _, c := range checks {
					rows = append(rows, []string{c.Name, c.Status, c.Detail})
				}
				return []string{"CHECK", "STATUS", "DETAIL"}, rows
			}, func() []string {
				var n []string
				for _, c := range checks {
					n = append(n, c.Name)
				}
				return n
			}); err != nil {
				return err
			}
			if bad > 0 {
				return fmt.Errorf("%d check(s) failed", bad)
			}
			return nil
		},
	}
}

func (a *app) serverCmd() *cobra.Command {
	sc := group("server", "Information about the server", "Commands about the daemon this client is connected to.", "jailops server info", "srv")
	sc.AddCommand(&cobra.Command{
		Use:   "info",
		Short: "Show the server's version, role, environment and extensions",
		Long: `Show what the server reports about itself: version, role (host or backup node),
whether it is a hub or clustered, how it sees this client, its environment
(operating system, ZFS, pool, PF) and the API extensions it implements.`,
		Example: "jailops server info\njailops --remote prod --host host-b server info -o yaml",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.connect(cmd.Context())
			if err != nil {
				return err
			}
			info, err := c.Info(cmd.Context())
			if err != nil {
				return err
			}
			if a.format != fmtTable {
				return a.emit(info, nil, nil, func() []string { return []string{info.Hostname} })
			}
			w := a.env.Stdout
			kv := func(k string, v any) { fprintf(w, "%-14s %v\n", k+":", v) }
			kv("Hostname", info.Hostname)
			kv("Host UUID", dash(info.HostUUID))
			kv("Version", info.Version)
			kv("API version", info.APIVersion)
			kv("Role", info.Role)
			kv("Hub", yesno(info.Hub))
			kv("Clustered", yesno(info.Clustered))
			kv("Seen as", info.Auth)
			kv("OS", fmt.Sprintf("%s %s (%s)", info.Environment.OS, info.Environment.KernelVersion, info.Environment.Arch))
			kv("ZFS", fmt.Sprintf("%s, pool %s", dash(info.Environment.ZFSVersion), dash(info.Environment.Pool)))
			kv("PF", yesno(info.Environment.PF))
			kv("Linuxulator", yesno(info.Environment.Linuxulator))
			kv("Extensions", strings.Join(info.APIExtensions, ", "))
			return nil
		},
	})
	return sc
}

func (a *app) versionCmd() *cobra.Command {
	var clientOnly bool
	cmd := &cobra.Command{
		Use:     "version",
		Short:   "Print client and server versions",
		Long:    "Print the version of this client, of the SDK and API it speaks and, unless --client is given, of the server it is connected to. An unreachable server is not an error here.",
		Example: "jailops version\njailops version --client -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := map[string]string{"client": a.env.Version, "sdk": sdk.Version, "api": api.Version}
			if !clientOnly {
				if c, err := a.connect(cmd.Context()); err == nil {
					if info, err := c.Info(cmd.Context()); err == nil {
						out["server"] = info.Version
					}
				}
			}
			if a.format != fmtTable {
				return a.emit(out, nil, nil, nil)
			}
			fprintf(a.env.Stdout, "Client:  %s\nSDK:     %s\nAPI:     %s\n", out["client"], out["sdk"], out["api"])
			if !clientOnly {
				fprintf(a.env.Stdout, "Server:  %s\n", dash(out["server"]))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&clientOnly, "client", false, "print client information only; do not contact a server")
	return cmd
}

func (a *app) completionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "completion bash|zsh|fish",
		Short: "Generate a shell completion script",
		Long: `Print a completion script for a shell. Commands and flags complete on their
own; names of jails, images, networks, projects, backups and operations are
completed from the server you are connected to, and when no server answers the
shell simply gets no names.

The packages install the scripts, so a packaged client needs nothing from this
command: /usr/local/share/bash-completion/completions/jailops,
/usr/local/share/zsh/site-functions/_jailops and
/usr/local/share/fish/vendor_completions.d/jailops.fish (bash needs the
bash-completion package; zsh needs compinit). For a binary installed by hand:

bash:  source <(jailops completion bash)       (or save it under /usr/local/share/bash-completion/completions/jailops)
zsh:   jailops completion zsh > "${fpath[1]}/_jailops"
fish:  jailops completion fish > ~/.config/fish/completions/jailops.fish`,
		Example:   "source <(jailops completion bash)\njailops completion zsh > ~/.zfunc/_jailops",
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"bash", "zsh", "fish"},
		RunE: func(cmd *cobra.Command, args []string) error {
			switch args[0] {
			case "bash":
				return cmd.Root().GenBashCompletionV2(a.env.Stdout, true)
			case "zsh":
				return cmd.Root().GenZshCompletion(a.env.Stdout)
			case "fish":
				return cmd.Root().GenFishCompletion(a.env.Stdout, true)
			}
			return usagef("unknown shell %q (use bash, zsh or fish)", args[0])
		},
	}
}

func (a *app) auditCmd() *cobra.Command {
	var limit int
	return &cobra.Command{
		Use:   "audit",
		Short: "Show the newest audit log entries of the server",
		Long: `Show who changed what: the newest entries of the server's audit log, newest
last. Every state change is audited with the requestor (user, token name or
certificate fingerprint), the action and the result. Reading the log needs the
admin role.`,
		Example: "jailops audit\njailops audit --limit 200 -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.connect(cmd.Context())
			if err != nil {
				return err
			}
			if err := c.Require(cmd.Context(), "audit_log"); err != nil {
				return err
			}
			items, err := c.Audit(cmd.Context(), limit)
			if err != nil {
				return err
			}
			return a.emit(nonNil(items), nil, func() ([]string, [][]string) {
				var rows [][]string
				for _, e := range items {
					rows = append(rows, []string{e.Timestamp.UTC().Format("2006-01-02 15:04:05"), e.Requestor, e.Action, dash(e.Resource), dash(e.Result)})
				}
				return []string{"TIME", "REQUESTOR", "ACTION", "RESOURCE", "RESULT"}, rows
			}, func() []string {
				var n []string
				for _, e := range items {
					n = append(n, e.ID)
				}
				return n
			})
		},
	}
}
