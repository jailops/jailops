package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"

	"jailops.net/jailops/internal/fleet/secrets"
	"jailops.net/jailops/internal/fleet/vault"
)

func (a *app) cmdSecrets() *cobra.Command {
	root := &cobra.Command{
		Use:   "secrets",
		Short: "Manage the age-encrypted secrets of the repository",
		Long: `Secrets live in secrets/<name>.age, encrypted to the age public keys listed in
secrets/recipients.txt. The repository holds ciphertext only. A jail
environment value "secret:<name>" is sent to the API sealed, as that
ciphertext; sync decrypts a secret itself only for host and hub tokens
(token_secret).

Values for jails can also live in shared/secrets.sops.yaml (SOPS): see
'jailops-sync vault --help'. A reference with a dot, "secret:app.db_password",
names such a value; a name without a dot names secrets/<name>.age.`,
	}
	root.AddCommand(
		&cobra.Command{
			Use: "list", Short: "List the secrets", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				f, err := a.load()
				if err != nil {
					return err
				}
				for _, n := range f.SecretNames() {
					fmt.Fprintln(cmd.OutOrStdout(), n)
				}
				return nil
			},
		},
		&cobra.Command{
			Use:   "check",
			Short: "Prove the identity opens every secret (prints no value)",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				f, err := a.load()
				if err != nil {
					return err
				}
				st := a.store(f)
				bad := 0
				for _, n := range f.SecretNames() {
					if _, err := st.Decrypt(n); err != nil {
						bad++
						fmt.Fprintf(cmd.OutOrStdout(), "FAIL  %s: %v\n", n, err)
						continue
					}
					fmt.Fprintf(cmd.OutOrStdout(), "ok    %s\n", n)
				}
				return failure(bad)
			},
		},
		&cobra.Command{
			Use:   "set <name>",
			Short: "Encrypt a value read from stdin (or a hidden prompt) to secrets/<name>.age",
			Example: `jailops-sync secrets set db-password
printf '%s' "$TOKEN" | jailops-sync secrets set api-token`,
			Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				if err := checkSecretName(args[0]); err != nil {
					return err
				}
				p, err := a.path()
				if err != nil {
					return err
				}
				st := secrets.NewStore(filepath.Join(p, "secrets"), secrets.Source{File: a.identity, Getenv: a.env.Getenv}, a.red)
				recips, err := st.Recipients()
				if err != nil {
					return err
				}
				val, err := a.readSecretValue(cmd)
				if err != nil {
					return err
				}
				a.red.Add(val)
				if err := st.Set(args[0], val, recips); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "wrote secrets/%s.age (%d recipient(s))\n", args[0], len(recips))
				return nil
			},
		},
		a.cmdSecretsAddHosts(),
		a.cmdSecretsPut(),
		&cobra.Command{
			Use:   "keygen",
			Short: "Generate an age identity: private key on stdout, public key on stderr",
			Long: `Prints a new identity to stdout. Redirect it to a file with mode 600 and
point --identity at it; put the public key (printed to stderr) in
secrets/recipients.txt. The private key is shown once and never stored by sync.`,
			Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				id, err := secrets.GenerateIdentity()
				if err != nil {
					return err
				}
				fmt.Fprintf(a.env.Stdout, "# public key: %s\n%s\n", id.Recipient(), id)
				fmt.Fprintf(a.env.Stderr, "public key: %s\n", id.Recipient())
				return nil
			},
		},
	)
	return root
}

func checkSecretName(n string) error {
	if n == "" || strings.ContainsAny(n, "/\\.") {
		return fmt.Errorf("invalid secret name %q", n)
	}
	return nil
}

func (a *app) readSecretValue(cmd *cobra.Command) ([]byte, error) {
	if f, ok := a.env.Stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(a.env.Stderr, "value (hidden): ")
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(a.env.Stderr)
		return b, err
	}
	b, err := io.ReadAll(io.LimitReader(a.env.Stdin, 1<<20))
	if err != nil {
		return nil, err
	}
	b = []byte(strings.TrimRight(string(b), "\r\n"))
	if len(b) == 0 {
		return nil, fmt.Errorf("empty value")
	}
	return b, nil
}

// cmdSecretsAddHosts asks each host for the age public key it publishes (server info, extension sealed_secrets) and
// lists it in secrets/recipients.txt, so that secrets sealed for jail environments can be opened by that host.
func (a *app) cmdSecretsAddHosts() *cobra.Command {
	var hosts []string
	cmd := &cobra.Command{
		Use:   "add-hosts",
		Short: "List the secrets keys of the hosts in secrets/recipients.txt",
		Long: `Each JailOps host holds an age key for the secrets of its jails and publishes the
public half in its server info. add-hosts reads it from the hosts (all, or
--host) and appends the ones that are missing to secrets/recipients.txt, with a
comment naming the host. New secrets are then encrypted to the hosts as well;
re-run 'secrets set' for an existing secret to include a new host.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			f, err := a.load()
			if err != nil {
				return err
			}
			hs, err := hostsOf(f, hosts)
			if err != nil {
				return err
			}
			p, err := a.path()
			if err != nil {
				return err
			}
			file := filepath.Join(p, "secrets", secrets.RecipientsFile)
			cur, _ := os.ReadFile(file)
			s := a.session(f)
			defer s.Close()
			var add strings.Builder
			for _, h := range hs {
				cl, err := s.Client(cmd.Context(), h)
				if err != nil {
					return fmt.Errorf("host %s: %w", h, err)
				}
				info, err := cl.Info(cmd.Context())
				if err != nil {
					return fmt.Errorf("host %s: %w", h, err)
				}
				if info.SecretsRecipient == "" {
					fmt.Fprintf(cmd.OutOrStdout(), "%-20s publishes no secrets key (an older version)\n", h)
					continue
				}
				if strings.Contains(string(cur), info.SecretsRecipient) {
					fmt.Fprintf(cmd.OutOrStdout(), "%-20s already listed\n", h)
					continue
				}
				fmt.Fprintf(&add, "# host %s\n%s\n", h, info.SecretsRecipient)
				fmt.Fprintf(cmd.OutOrStdout(), "%-20s added\n", h)
			}
			if add.Len() == 0 {
				return nil
			}
			if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
				return err
			}
			out := string(cur)
			if out != "" && !strings.HasSuffix(out, "\n") {
				out += "\n"
			}
			return os.WriteFile(file, []byte(out+add.String()), 0o644)
		},
	}
	cmd.Flags().StringSliceVar(&hosts, "host", nil, "limit to these hosts (names or @groups)")
	return cmd
}

// cmdSecretsPut sets one value of shared/secrets.sops.yaml without needing the sops program: the file is decrypted
// with the operator's key, the value is set, and the file is encrypted again under .sops.yaml.
func (a *app) cmdSecretsPut() *cobra.Command {
	return &cobra.Command{
		Use:   "put <dotted.path>",
		Short: "Set a value of shared/secrets.sops.yaml (read from stdin or a hidden prompt)",
		Long: `Sets the value at a dotted path (for example app.db_password) in
shared/secrets.sops.yaml, creating the file when it does not exist. The key is found
through SOPS_AGE_KEY, SOPS_AGE_KEY_FILE or SOPS_AGE_KEY_CMD; who can decrypt is
decided by .sops.yaml. A jail refers to the value as secret:<dotted.path>.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := args[0]
			parts := strings.Split(path, ".")
			for _, part := range parts {
				if part == "" || strings.ContainsAny(part, " /\\") {
					return fmt.Errorf("invalid path %q", path)
				}
			}
			p, err := a.path()
			if err != nil {
				return err
			}
			if err := vault.EnsureUnlocked(a.env.getenv); err != nil {
				return err
			}
			m, err := vault.LoadSecrets(p, a.env.getenv)
			if err != nil {
				return err
			}
			val, err := a.readSecretValue(cmd)
			if err != nil {
				return err
			}
			a.red.Add(val)
			cur := m
			for _, part := range parts[:len(parts)-1] {
				next, ok := cur[part].(map[string]any)
				if !ok {
					if _, exists := cur[part]; exists {
						return fmt.Errorf("%s is a value, not a group of values", part)
					}
					next = map[string]any{}
					cur[part] = next
				}
				cur = next
			}
			if _, isMap := cur[parts[len(parts)-1]].(map[string]any); isMap {
				return fmt.Errorf("%s is a group of values, not a value", path)
			}
			cur[parts[len(parts)-1]] = string(val)
			plain, err := yaml.Marshal(m)
			if err != nil {
				return err
			}
			enc, err := vault.EncryptYAML(p, vault.SecretsRel, plain)
			if err != nil {
				return err
			}
			if err := vault.WriteFileAtomic(vault.SecretsPath(p), enc, 0o644); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "set %s in %s\n", path, vault.SecretsRel)
			return nil
		},
	}
}
