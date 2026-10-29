package cli

import (
	"github.com/spf13/cobra"

	"jailops.net/jailops/internal/manpage"
)

// cmdMan is hidden: the packages run it at build time to write the man pages (one mdoc page per command).
func (a *app) cmdMan() *cobra.Command {
	return &cobra.Command{
		Use:    "man DIR",
		Short:  "Write the man pages of every command into DIR",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return manpage.Write(cmd.Root(), args[0], "jailops(1)")
		},
	}
}
