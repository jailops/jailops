package cli

import (
	"strings"

	"github.com/spf13/cobra"

	"jailops.net/jailops/internal/fleet/config"
)

// Completion functions run without the persistent flags being validated and
// must never print errors: a failure just yields no suggestions.

func (a *app) quietLoad() *config.Fleet {
	p, err := a.path()
	if err != nil {
		return nil
	}
	f, err := config.Load(p)
	if err != nil {
		return nil
	}
	return f
}

func (a *app) completeHosts(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	f := a.quietLoad()
	if f == nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []string
	for _, h := range f.HostNames() {
		out = append(out, h)
	}
	for _, g := range f.GroupNames() {
		out = append(out, "@"+g)
	}
	var match []string
	for _, s := range out {
		if strings.HasPrefix(s, toComplete) {
			match = append(match, s)
		}
	}
	return match, cobra.ShellCompDirectiveNoFileComp
}

func (a *app) completeRollouts(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	f := a.quietLoad()
	if f == nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return f.RolloutNames(), cobra.ShellCompDirectiveNoFileComp
}
