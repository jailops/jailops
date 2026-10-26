package lifecycle

import (
	"strings"

	"jailops.net/jailops/internal/validate"
)

// A jail key is "<project>_<name>": every jail belongs to a named project and neither part holds an underscore.

// qualify builds the key of a jail of a project.
func qualify(project, name string) (string, error) {
	if project == "" {
		return "", validate.Invalid("project", "a jail belongs to a project: the project is required")
	}
	if err := validate.Name("project", project); err != nil {
		return "", err
	}
	if err := validate.Name("name", name); err != nil {
		return "", err
	}
	k := project + "_" + name
	if len(k) > validate.MaxJailKeyLen {
		return "", validate.Invalid("name", "project %q and jail %q are too long together", project, name).
			WithHint("the project and jail names together may be at most 63 characters")
	}
	return k, nil
}

// splitKey inverts qualify. A key without a project gives empty parts.
func splitKey(key string) (project, name string) {
	p, n, ok := strings.Cut(key, "_")
	if !ok || p == "" || n == "" {
		return "", ""
	}
	return p, n
}
