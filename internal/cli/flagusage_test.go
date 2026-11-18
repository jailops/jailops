package cli_test

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"jailops.net/jailops/internal/cli"
)

// pflag takes the first backquoted word of a flag's usage as the name of the flag's value, so a backquote in a usage string
// turns "--memory 2G" into "--memory jailops info JAIL" in the help. No flag of any command has one.
func TestNoFlagUsageHasABackquote(t *testing.T) {
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		check := func(f *pflag.Flag) {
			if strings.Contains(f.Usage, "`") {
				t.Errorf("%s: flag --%s has a backquote in its usage: %q", c.CommandPath(), f.Name, f.Usage)
			}
		}
		c.LocalFlags().VisitAll(check)
		c.PersistentFlags().VisitAll(check)
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(cli.NewRootCommand(&cli.Env{Getenv: func(string) string { return "" }}))
}
