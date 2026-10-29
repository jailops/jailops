package cli

import (
	"github.com/spf13/cobra"

	"jailops.net/jailops/internal/manpage"
)

// manCmd is hidden: the packages run it at build time to write the man pages (one mdoc page per command).
func (a *app) manCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "man DIR",
		Short: "Write the man pages of every command into DIR",
		Long: `Write one mdoc page per command into DIR (jailops.1, jailops-jail-list.1, ...),
generated from the command tree: the packages run this at build time and install
the pages under share/man/man1. Set SOURCE_DATE_EPOCH for a reproducible date.`,
		Example: "jailops man ./man1\nSOURCE_DATE_EPOCH=0 jailops man /tmp/man1",
		Hidden:  true,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return manpage.Write(cmd.Root(), args[0], "jailops-sync(1)", "jailopsd(8)", "jailops-priv(8)")
		},
	}
}
