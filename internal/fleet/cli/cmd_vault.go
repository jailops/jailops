package cli

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"jailops.net/jailops/internal/fleet/vault"
)

// printCommitReminder says, after a change to .sops.yaml or to re-wrapped
// files, that none of it reaches anyone else's clone until it is committed.
func printCommitReminder(w io.Writer, paths ...string) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, "COMMIT THIS NOW: nothing here takes effect for anyone else until it is committed and pushed.")
	fmt.Fprintf(w, "  git add %s\n", strings.Join(paths, " "))
	fmt.Fprintln(w, "  git commit -m 'sops: update recipients'")
}

func (a *app) cmdVault() *cobra.Command {
	root := &cobra.Command{
		Use:   "vault",
		Short: "Inspect the SOPS key and manage the recipients in .sops.yaml",
		Long: `The SOPS files of the repository (shared/secrets.sops.yaml and
hosts/<host>/remote.sops.yaml) are decrypted with an age key that SOPS finds
through one of its own environment variables:

  SOPS_AGE_KEY       the key text
  SOPS_AGE_KEY_FILE  a file with an age identity, or an age plugin identity
                     (AGE-PLUGIN-<NAME>-1...) for a hardware token, an agent or
                     a secret manager, with no key at rest
  SOPS_AGE_KEY_CMD   a command that prints the key

jailops-sync does no cryptography of its own and has no other way to get a key.
SOPS also reads ~/.config/sops/age/keys.txt when it exists; that implicit file is
not treated as configured here, so set SOPS_AGE_KEY_FILE even for that path.

Who can decrypt is the list of age public keys in .sops.yaml. The commands below
edit that file and re-wrap the encrypted files for the new list. They hold no
secret and need the operator to be a recipient already (re-wrapping decrypts the
data key).`,
	}
	root.AddCommand(a.vaultStatus(), a.vaultList(), a.vaultAdd(), a.vaultRemove())
	return root
}

func (a *app) vaultStatus() *cobra.Command {
	return &cobra.Command{
		Use: "status", Short: "Show which SOPS key variable is set", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := a.path()
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "fleet path  %s\n", p)
			b := vault.Resolve(a.env.getenv)
			fmt.Fprintf(w, "key         %s\n", b.Describe())
			if b.Name == "" {
				fmt.Fprintln(w)
				fmt.Fprintln(w, vault.ErrNoKey())
				return &ExitError{Code: ExitFailure}
			}
			return vault.EnsureUnlocked(a.env.getenv)
		},
	}
}

func (a *app) vaultList() *cobra.Command {
	return &cobra.Command{
		Use: "list-recipients", Short: "List the age public keys of .sops.yaml and the rules each opens", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := a.path()
			if err != nil {
				return err
			}
			pol, err := vault.LoadSopsPolicy(p)
			if err != nil {
				return err
			}
			rs := pol.ListRecipients()
			if len(rs) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "(no recipients in .sops.yaml)")
				return nil
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ANCHOR\tCOMMENT\tRULES\tPUBLIC KEY")
			for _, r := range rs {
				c, rules := r.Comment, strings.Join(r.Rules, ", ")
				if c == "" {
					c = "-"
				}
				if rules == "" {
					rules = "(none: orphaned entry)"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.Anchor, c, rules, r.PubKey)
			}
			return tw.Flush()
		},
	}
}

func (a *app) vaultAdd() *cobra.Command {
	var comment string
	var rules []string
	c := &cobra.Command{
		Use:   "add-recipient ANCHOR AGE1PUBKEY",
		Short: "Add an age public key and re-wrap the encrypted files for it",
		Long: `Adds the key to .sops.yaml under the anchor (its label: say whose key or which
machine it is, for example ops_laptop or ci_runner), wires it into every
creation_rule (or only those whose path_regex equals a --rule), and re-wraps
every already-encrypted file those rules cover, so existing secrets open for the
new recipient at once.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.path()
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			pol, err := vault.LoadSopsPolicy(p)
			if err != nil {
				return err
			}
			if err := pol.AddRecipient(args[0], args[1], comment, vault.AddRecipientOptions{Rules: rules}); err != nil {
				return err
			}
			affected := rules
			if len(affected) == 0 {
				for _, r := range pol.ListRecipients() {
					if r.Anchor == args[0] {
						affected = r.Rules
					}
				}
			}
			files, err := vault.AffectedFiles(p, affected)
			if err != nil {
				return err
			}
			if err := pol.Save(); err != nil {
				return err
			}
			fmt.Fprintf(w, "added recipient %s (%s) to %s\n", args[0], args[1], vault.SopsPolicyFilename)
			return a.rewrap(w, p, files, args[0], false)
		},
	}
	c.Flags().StringVar(&comment, "comment", "", "label for the key, written above it in .sops.yaml")
	c.Flags().StringArrayVar(&rules, "rule", nil, "wire the key only into the creation_rule with exactly this path_regex (repeatable; default every rule)")
	return c
}

func (a *app) vaultRemove() *cobra.Command {
	return &cobra.Command{
		Use:   "remove-recipient ANCHOR|AGE1PUBKEY",
		Short: "Remove a recipient and re-wrap (real revocation)",
		Long: `Removes the recipient from .sops.yaml and from every creation_rule, then
re-wraps every affected encrypted file for the remaining recipients. The re-wrap
is what revokes: deleting the line alone would leave the old ciphertext open to
whoever kept the key. Refuses to leave a creation_rule without recipients, which
would make its files unreadable for everyone.

Remember that a removed key could already have read the current secret values:
rotate the secrets themselves when the key may have leaked.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.path()
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			pol, err := vault.LoadSopsPolicy(p)
			if err != nil {
				return err
			}
			var affected []string
			found := false
			for _, r := range pol.ListRecipients() {
				if r.Anchor == args[0] || r.PubKey == args[0] {
					affected, found = r.Rules, true
				}
			}
			if found && len(affected) == 0 {
				return fmt.Errorf("%q is in keys: but no creation_rule uses it, so there is nothing to re-wrap; remove the stale entry from %s by hand", args[0], vault.SopsPolicyFilename)
			}
			anchor, err := pol.RemoveRecipient(args[0])
			if err != nil {
				return err
			}
			files, err := vault.AffectedFiles(p, affected)
			if err != nil {
				return err
			}
			if err := pol.Save(); err != nil {
				return err
			}
			fmt.Fprintf(w, "removed recipient %s from %s\n", anchor, vault.SopsPolicyFilename)
			return a.rewrap(w, p, files, anchor, true)
		},
	}
}

// rewrap re-wraps files and reports exactly how far it got. After a removal a
// failure is serious: the recipient can still read what was not re-wrapped, so
// the commit reminder is withheld.
func (a *app) rewrap(w io.Writer, fleetPath string, files []string, who string, removal bool) error {
	if len(files) == 0 {
		fmt.Fprintln(w, "no already-encrypted file matched: nothing to re-wrap yet")
		printCommitReminder(w, vault.SopsPolicyFilename)
		return nil
	}
	if err := vault.EnsureUnlocked(a.env.getenv); err != nil {
		return fmt.Errorf("re-wrapping needs a key that is already a recipient: %w\n.sops.yaml was changed; re-run after setting the key is not possible, so review it with git diff", err)
	}
	done, failed, err := vault.UpdateKeys(fleetPath, files)
	if len(done) > 0 {
		fmt.Fprintf(w, "re-wrapped %d file(s):\n", len(done))
		for _, f := range done {
			fmt.Fprintf(w, "  %s\n", f)
		}
	}
	if err != nil {
		if removal {
			return fmt.Errorf("%v\n  FAILED: %s\nDo NOT commit yet: %s can still decrypt the files that were not re-wrapped", err, failed, who)
		}
		printCommitReminder(w, append([]string{vault.SopsPolicyFilename}, done...)...)
		return fmt.Errorf("%v\n  FAILED: %s\n%s opens what is listed above; fix the cause and run the command again for the rest", err, failed, who)
	}
	printCommitReminder(w, append([]string{vault.SopsPolicyFilename}, done...)...)
	return nil
}
