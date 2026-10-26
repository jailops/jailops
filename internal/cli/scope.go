package cli

import (
	"strings"

	"github.com/spf13/cobra"
)

// scopeFlags registers --projects and --global for a command that creates an
// identity. The returned function reads them and refuses a missing or
// contradictory scope: an empty scope grants nothing, so it is never sent.
func scopeFlags(cmd *cobra.Command) func() ([]string, bool, error) {
	var projects []string
	var global bool
	cmd.Flags().StringSliceVar(&projects, "projects", nil, "projects the identity may act in, comma separated")
	cmd.Flags().BoolVar(&global, "global", false, "access to every project and to host-level objects (global admins only)")
	return func() ([]string, bool, error) {
		var ps []string
		for _, p := range projects {
			if p = strings.TrimSpace(p); p != "" {
				ps = append(ps, p)
			}
		}
		switch {
		case global && len(ps) > 0:
			return nil, false, usagef("--projects and --global exclude each other: choose the projects, or global access")
		case !global && len(ps) == 0:
			return nil, false, usagef("an identity needs a scope: pass --projects a,b or --global (no scope means no access, so nothing is created)")
		}
		return ps, global, nil
	}
}

// scopeText renders a scope for tables.
func scopeText(projects []string, global bool) string {
	if global {
		return "global"
	}
	return joinOrDash(projects)
}
