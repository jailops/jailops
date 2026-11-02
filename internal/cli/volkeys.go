package cli

import (
	"github.com/spf13/cobra"

	"jailops.net/jailops/sdk/api"
)

// Encrypted volumes: the passphrase is typed on the terminal without echo, or read from a file for scripts. It is
// never a command line argument (it would be in the shell history and in ps).

func hasEncryptedVolume(sp api.JailSpec) bool {
	for _, v := range sp.Volumes {
		if v.Encrypted {
			return true
		}
	}
	return false
}

// readPassphraseFile reads a passphrase from the first line of a file, or of standard input for "-".
func (a *app) readPassphraseFile(path string) (string, error) {
	return a.readSecretFile("passphrase", path)
}

// promptPassphrase asks on the terminal without echo; a pipe is refused.
func (a *app) promptPassphrase(prompt string) (string, error) {
	if !a.env.IsTTY(a.env.Stdin) {
		return "", usagef("standard input is not a terminal: give the passphrase with --passphrase-file FILE")
	}
	return a.env.ReadSecret(prompt)
}

// newPassphrase reads a new passphrase: from file, else asked twice. It must be 12 to 512 bytes long.
func (a *app) newPassphrase(file, prompt string) (string, error) {
	var p string
	if file != "" {
		var err error
		if p, err = a.readPassphraseFile(file); err != nil {
			return "", err
		}
	} else {
		first, err := a.promptPassphrase(prompt)
		if err != nil {
			return "", err
		}
		second, err := a.promptPassphrase("Repeat the passphrase: ")
		if err != nil {
			return "", err
		}
		if first != second {
			return "", usagef("the passphrases do not match")
		}
		p = first
	}
	if err := api.CheckPassphrase(p); err != nil {
		return "", usagef("%s", err.(*api.Error).Message)
	}
	return p, nil
}

// newVolumePassphrase is the passphrase of the encrypted volumes of a jail being created.
func (a *app) newVolumePassphrase() (string, error) {
	if a.passFile == "" && a.env.IsTTY(a.env.Stdin) {
		fprintln(a.env.Stderr, "The encrypted volumes need a passphrase. It is asked for after every host boot and cannot be recovered when lost.")
	}
	return a.newPassphrase(a.passFile, "Passphrase for the encrypted volumes: ")
}

// givenPassphrase reads an existing passphrase: from file, else asked once.
func (a *app) givenPassphrase(file, prompt string) (string, error) {
	if file != "" {
		return a.readPassphraseFile(file)
	}
	p, err := a.promptPassphrase(prompt)
	if err == nil && p == "" {
		err = usagef("the passphrase is empty")
	}
	return p, err
}

func (a *app) jailUnlock(d *kindDesc[api.Jail]) *cobra.Command {
	var start bool
	var file string
	cmd := &cobra.Command{
		Use:   "unlock NAME",
		Short: "Type the passphrase of a jail's encrypted volumes",
		Long: `Load the keys of a jail's encrypted volumes after a host boot or a lock. The
passphrase is asked on the terminal without echo (--passphrase-file reads it from
a file for scripts). With --start the jail starts once it is unlocked; a jail
that was due to start at boot (autostart) starts after its unlock anyway.

A wrong passphrase answers bad_passphrase. After five wrong passphrases in a row
the jail refuses unlocks for 30 seconds.`,
		Example:           "jailops jail unlock vault\njailops jail unlock vault --start\njailops jail unlock vault --passphrase-file /root/vault.pass",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeKind(a, d),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, name := cmd.Context(), args[0]
			if ok, err := a.dry("unlock jail "+name, nil); ok || err != nil {
				return err
			}
			c, err := a.connect(ctx)
			if err != nil {
				return err
			}
			p, err := a.givenPassphrase(file, "Passphrase of "+name+": ")
			if err != nil {
				return err
			}
			h, err := handle(c.Jails().Unlock(ctx, name, p, start))
			if err != nil {
				return err
			}
			verb := "unlocked"
			if start {
				verb = "unlocked and started"
			}
			return a.finishOp(ctx, h, "jail", name, verb, nil)
		},
	}
	cmd.Flags().BoolVar(&start, "start", false, "start the jail once it is unlocked")
	cmd.Flags().StringVar(&file, "passphrase-file", "", "read the passphrase from this file (its first line) instead of asking")
	return cmd
}

func (a *app) jailLock(d *kindDesc[api.Jail]) *cobra.Command {
	return &cobra.Command{
		Use:   "lock NAME",
		Short: "Stop a jail and lock its encrypted volumes",
		Long: `Stop a jail, unmount its datasets and unload the keys of its encrypted volumes.
The data is then unreadable on this host until 'jailops jail unlock' is given the
passphrase again.`,
		Example:           "jailops jail lock vault",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeKind(a, d),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, name := cmd.Context(), args[0]
			if ok, err := a.dry("lock jail "+name, nil); ok || err != nil {
				return err
			}
			c, err := a.connect(ctx)
			if err != nil {
				return err
			}
			h, err := handle(c.Jails().Lock(ctx, name))
			if err != nil {
				return err
			}
			return a.finishOp(ctx, h, "jail", name, "locked", nil)
		},
	}
}

func (a *app) jailPassphrase(d *kindDesc[api.Jail]) *cobra.Command {
	var oldFile, newFile string
	cmd := &cobra.Command{
		Use:   "passphrase NAME",
		Short: "Change the passphrase of a jail's encrypted volumes",
		Long: `Change the passphrase of every encrypted volume of an unlocked jail (zfs
change-key). The current passphrase is asked first, then the new one twice. The
data is not encrypted again: the passphrase protects the key, and the key stays.
Scripts give --passphrase-file (the current one) and --new-passphrase-file.`,
		Example:           "jailops jail passphrase vault",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeKind(a, d),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, name := cmd.Context(), args[0]
			if ok, err := a.dry("change the volume passphrase of jail "+name, nil); ok || err != nil {
				return err
			}
			c, err := a.connect(ctx)
			if err != nil {
				return err
			}
			old, err := a.givenPassphrase(oldFile, "Current passphrase of "+name+": ")
			if err != nil {
				return err
			}
			nw, err := a.newPassphrase(newFile, "New passphrase: ")
			if err != nil {
				return err
			}
			h, err := handle(c.Jails().ChangePassphrase(ctx, name, old, nw))
			if err != nil {
				return err
			}
			return a.finishOp(ctx, h, "jail", name, "passphrase changed", nil)
		},
	}
	cmd.Flags().StringVar(&oldFile, "passphrase-file", "", "read the current passphrase from this file instead of asking")
	cmd.Flags().StringVar(&newFile, "new-passphrase-file", "", "read the new passphrase from this file instead of asking")
	return cmd
}
