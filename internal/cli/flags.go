package cli

import (
	"github.com/spf13/cobra"
)

// aliasFlags lets cmd accept other spellings of its flags without listing them: with {"permission": "role"}
// --permission reads as --role. Old spellings stay working this way, so scripts do not break; the help, the man
// pages and completion show the one name. An alias is never registered as a flag of its own.
func aliasFlags(cmd *cobra.Command, aliases map[string]string) {
	fs := cmd.Flags()
	fs.SetNormalizeFunc(withAliases(fs.GetNormalizeFunc(), aliases))
}

// withAliases wraps a flag set's name normalisation with the aliases (generic so the client names no flag package).
func withAliases[F ~func(S, string) N, S any, N ~string](prev F, aliases map[string]string) F {
	return func(f S, name string) N {
		if to, ok := aliases[name]; ok {
			name = to
		}
		return prev(f, name)
	}
}

// roles are the values of --role.
var roles = []string{"readonly", "operator", "admin", "backup-push", "backup-pull", "restore", "backup-admin"}

// roleFlag registers --role (--permission, its former name, still works).
func roleFlag(cmd *cobra.Command, p *string, def, usage string) {
	if usage == "" {
		usage = "role: readonly, operator, admin, backup-push, backup-pull, restore or backup-admin"
	}
	cmd.Flags().StringVar(p, "role", def, usage)
	aliasFlags(cmd, map[string]string{"permission": "role"})
	enumFlag(cmd, "role", roles...)
}
