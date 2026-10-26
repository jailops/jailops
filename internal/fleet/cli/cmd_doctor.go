package cli

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/spf13/cobra"

	"jailops.net/jailops/internal/fleet/config"
	"jailops.net/jailops/internal/fleet/plan"
	"jailops.net/jailops/internal/fleet/secrets"
	"jailops.net/jailops/internal/fleet/vault"
	"jailops.net/jailops/sdk"
)

func (a *app) cmdDoctor() *cobra.Command {
	var deep bool
	var hosts []string
	c := &cobra.Command{
		Use:   "doctor",
		Short: "Check the tool, the repository, the identity and every host",
		Long: `Runs a series of checks and prints ok or FAIL for each: the repository loads
and validates, the age identity is configured, every host answers and
trusts the credential, and git is available. With --deep it also decrypts
each secret, the SOPS secrets file and every remote file to prove the key opens
them (nothing is printed or stored).
Exit status 1 when any check fails.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			w := cmd.OutOrStdout()
			failed := 0
			check := func(name string, err error, okMsg string) {
				if err != nil {
					failed++
					fmt.Fprintf(w, "FAIL  %-28s %v\n", name, err)
					return
				}
				fmt.Fprintf(w, "ok    %-28s %s\n", name, okMsg)
			}
			p, perr := a.path()
			check("fleet path", perr, p)
			var f *config.Fleet
			if perr == nil {
				var err error
				f, err = config.Load(p)
				check("repository layout", err, "")
				if err == nil {
					check("repository validation", f.Check(), fmt.Sprintf("%d host(s), %d object(s)", len(f.Hosts), len(f.Objects)))
					for _, wmsg := range f.Warnings {
						fmt.Fprintf(w, "warn  %s\n", wmsg)
					}
				}
			}
			_, gerr := exec.LookPath("git")
			check("git binary", gerr, "found (needed by watch and the repository hygiene check)")
			if f == nil {
				return failure(failed)
			}
			store := a.store(f)
			src := secrets.Source{File: a.identity, Getenv: a.env.Getenv}
			_, ierr := src.Load()
			needs := len(f.Secrets) > 0
			if ierr != nil && !needs && ierr == secrets.ErrNoIdentity {
				fmt.Fprintf(w, "ok    %-28s none configured, none needed\n", "age identity")
			} else {
				check("age identity", ierr, src.Describe())
			}
			if deep && ierr == nil {
				for _, n := range f.SecretNames() {
					_, err := store.Decrypt(n)
					check("secret "+n, err, "decrypts")
				}
			}
			// The SOPS key: needed when the repository holds a SOPS file.
			sopsNeeded := f.SopsSecrets != nil || len(f.Remotes) > 0
			be := vault.Resolve(a.env.getenv)
			var kerr error
			if be.Name != "" || sopsNeeded {
				kerr = vault.EnsureUnlocked(a.env.getenv)
			}
			if be.Name == "" && !sopsNeeded {
				fmt.Fprintf(w, "ok    %-28s none configured, none needed\n", "sops key")
			} else {
				check("sops key", kerr, be.Describe())
			}
			if deep && kerr == nil {
				if f.SopsSecrets != nil {
					_, err := vault.LoadSecrets(f.Path, a.env.getenv)
					check("sops "+vault.SecretsRel, err, "decrypts")
				}
				for _, h := range f.HostNames() {
					if f.Remotes[h] == nil {
						continue
					}
					r, err := vault.LoadRemote(f.Path, h, a.env.getenv)
					msg := ""
					if err == nil {
						var cfp string
						if cfp, err = r.ClientFingerprint(); err == nil {
							msg = "decrypts; client certificate " + cfp[:16] + "..."
						}
					}
					check("sops "+vault.RemoteRel(h), err, msg)
				}
			}
			hs, err := hostsOf(f, hosts)
			if err != nil {
				check("host selection", err, "")
				return failure(failed)
			}
			s := a.session(f)
			defer s.Close()
			for _, h := range hs {
				cl, err := s.Client(cmd.Context(), h)
				if err != nil {
					check("host "+h, err, "")
					continue
				}
				info, err := cl.Info(cmd.Context())
				if err == nil && info.Auth != "trusted" {
					err = fmt.Errorf("the server sees this client as %q; check the token or certificate", info.Auth)
				}
				check("host "+h, err, fmt.Sprintf("%s, api %s, %d extensions", info.Hostname, info.APIVersion, len(info.APIExtensions)))
			}
			if len(f.Projects) > 0 {
				pl := plan.Compute(cmd.Context(), s, plan.Options{Hosts: hs})
				for _, ph := range pl.ProjectHosts {
					if ph.Offline {
						failed++
						fmt.Fprintf(w, "FAIL  %-28s %s\n", "project "+ph.Project+" on "+ph.Host, ph.State)
					} else {
						fmt.Fprintf(w, "ok    %-28s %s\n", "project "+ph.Project+" on "+ph.Host, ph.State)
					}
				}
			}
			// Trust identities are credentials: they belong to the operator
			// and sync never manages them. Report the ones that name a
			// project this fleet does not define.
			for _, h := range hs {
				cl, err := s.Client(cmd.Context(), h)
				if err != nil {
					continue
				}
				for _, w2 := range unknownProjectIdentities(cmd.Context(), cl, f) {
					fmt.Fprintf(w, "warn  host %s: %s\n", h, w2)
				}
			}
			return failure(failed)
		},
	}
	c.Flags().BoolVar(&deep, "deep", false, "also decrypt every secret")
	a.hostFlag(c, &hosts)
	return c
}

func failure(n int) error {
	if n == 0 {
		return nil
	}
	return &ExitError{ExitFailure, fmt.Sprintf("%d check(s) failed", n)}
}

// unknownProjectIdentities lists certificates, tokens and users whose project
// scope names a project the fleet repository does not define. Listing needs a global admin; a failure is reported
// as one line, not as an error.
func unknownProjectIdentities(ctx context.Context, cl *sdk.Client, f *config.Fleet) []string {
	known := func(p string) bool { return f.Projects[p] != nil }
	var out []string
	check := func(kind, name string, projects []string) {
		for _, p := range projects {
			if !known(p) {
				out = append(out, fmt.Sprintf("%s %q is scoped to project %q, which the fleet does not define", kind, name, p))
			}
		}
	}
	for c, err := range cl.Certificates().List(ctx) {
		if err != nil {
			return append(out, "cannot list identities: "+err.Error())
		}
		check("certificate", c.Metadata.Name, c.Spec.Projects)
	}
	for t, err := range cl.Tokens().List(ctx) {
		if err != nil {
			return append(out, "cannot list tokens: "+err.Error())
		}
		check("token", t.Metadata.Name, t.Spec.Projects)
	}
	for u, err := range cl.Users().List(ctx) {
		if err != nil {
			return append(out, "cannot list users: "+err.Error())
		}
		check("user", u.Metadata.Name, u.Spec.Projects)
	}
	return out
}
