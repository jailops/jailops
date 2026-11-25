package validate

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"jailops.net/jailops/sdk/api"
)

var (
	recipePkgRE  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$`)
	recipeSvcRE  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	recipeUserRE = regexp.MustCompile(`^([a-z_][a-z0-9_-]{0,31}\$?|[0-9]{1,10})$`)
	recipeModeRE = regexp.MustCompile(`^(0[0-7]{3}|[0-7]{3})$`)
	recipeVarRE  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
)

const (
	// RecipeMaxSteps is the most steps of a recipe.
	RecipeMaxSteps = 200
	// RecipeMaxRequirements is the most requirements of a recipe.
	RecipeMaxRequirements = 50
	// RecipeFileMax and RecipeTotalMax are the bytes one file and all files of a recipe may carry.
	RecipeFileMax  = 1 << 20
	RecipeTotalMax = 8 << 20
)

// RecipeSysrcFiles are the files a sysrc step may change, below /etc of the jail. rc.conf.local is not one of them:
// JailOps writes it at every start.
var RecipeSysrcFiles = []string{"rc.conf", "periodic.conf"}

// recipeDenied are the directories of the base system: pkgbase owns what is below them, so a recipe does not write
// there. It is a rule against clobbering base files, not a security boundary: a run step can do anything its operator
// can do in the jail.
var recipeDenied = []string{"/etc", "/boot", "/bin", "/sbin", "/lib", "/libexec", "/rescue", "/usr/bin", "/usr/sbin", "/usr/lib", "/usr/libexec", "/usr/lib32", "/usr/libdata", "/usr/share", "/usr/include", "/usr/tests"}

// RecipeAllowedFiles are the files below /etc that FreeBSD expects an administrator to edit (the base ships them as
// defaults only) and that nothing of JailOps writes.
var RecipeAllowedFiles = []string{"/etc/sysctl.conf", "/etc/periodic.conf", "/etc/ssh/sshd_config", "/etc/newsyslog.conf"}

// RecipePathDenied says whether a recipe may not write the file at p (a clean absolute path).
func RecipePathDenied(p string) bool {
	for _, a := range RecipeAllowedFiles {
		if p == a {
			return false
		}
	}
	for _, d := range recipeDenied {
		if p == d || strings.HasPrefix(p, d+"/") {
			return true
		}
	}
	return false
}

// Recipe validates a recipe as the daemon receives it: a file carries its Content (the client read its Source).
// Nothing in it is a shell string: packages, services and users are names, files have absolute paths, a run is argv.
func Recipe(r api.Recipe) error {
	if len(r.Steps) == 0 || len(r.Steps) > RecipeMaxSteps {
		return Invalid("steps", "a recipe has between 1 and %d steps", RecipeMaxSteps)
	}
	if err := recipeRequirements(r.Requirements); err != nil {
		return err
	}
	total := 0
	for i, st := range r.Steps {
		fld := fmt.Sprintf("steps[%d]", i)
		kinds := 0
		for _, set := range []bool{st.Package != nil, st.File != nil, st.Dir != nil, st.Sysrc != nil, st.Service != nil, st.Run != nil} {
			if set {
				kinds++
			}
		}
		if kinds != 1 {
			return Invalid(fld, "%s: a step is exactly one of package, file, dir, sysrc, service and run", fld)
		}
		if (st.Creates != "" || st.Unless != nil) && st.Run == nil {
			return Invalid(fld, "%s: creates and unless belong to a run step", fld)
		}
		if st.In != "" && st.Sysrc == nil {
			return Invalid(fld, "%s: in belongs to a sysrc step", fld)
		}
		var err error
		switch {
		case st.Package != nil:
			err = recipePackages(fld, st.Package)
		case st.File != nil:
			var n int
			n, err = recipeFile(fld, *st.File)
			total += n
		case st.Dir != nil:
			err = recipeDir(fld, *st.Dir)
		case st.Sysrc != nil:
			err = recipeSysrc(fld, st)
		case st.Service != nil:
			err = recipeService(fld, *st.Service)
		default:
			err = recipeRun(fld, st)
		}
		if err != nil {
			return err
		}
	}
	if total > RecipeTotalMax {
		return Invalid("steps", "the files of a recipe are larger than %d MiB together", RecipeTotalMax>>20)
	}
	return nil
}

func recipePackages(fld string, names []string) error {
	if len(names) == 0 || len(names) > 200 {
		return Invalid(fld+".package", "%s.package: between 1 and 200 package names", fld)
	}
	for _, n := range names {
		if !recipePkgRE.MatchString(n) {
			return Invalid(fld+".package", "%s.package: a name is not a package name", fld)
		}
	}
	return nil
}

func recipeFile(fld string, f api.RecipeFile) (int, error) {
	fld += ".file"
	if f.Source != "" {
		return 0, Invalid(fld+".source", "%s.source: the client reads the source file; the daemon takes content", fld)
	}
	if f.Content == nil {
		return 0, Invalid(fld, "%s: a file needs a source or content", fld)
	}
	if err := AbsPath(fld+".path", f.Path); err != nil {
		return 0, err
	}
	if f.Path == "/" || RecipePathDenied(f.Path) {
		return 0, Invalid(fld+".path", "%s.path: the path belongs to the base system: put local configuration below /usr/local/etc", fld)
	}
	if len(*f.Content) > RecipeFileMax || !utf8.ValidString(*f.Content) || strings.ContainsRune(*f.Content, 0) {
		return 0, Invalid(fld+".content", "%s: a file is text of at most %d MiB (copy other files with jailops cp)", fld, RecipeFileMax>>20)
	}
	if err := recipeOwnerMode(fld, f.Owner, f.Group, f.Mode); err != nil {
		return 0, err
	}
	return len(*f.Content), nil
}

// recipeOwnerMode checks the owner, group and mode of a file or directory step.
func recipeOwnerMode(fld, owner, group, mode string) error {
	for _, x := range [][2]string{{"owner", owner}, {"group", group}} {
		if x[1] != "" && !recipeUserRE.MatchString(x[1]) {
			return Invalid(fld+"."+x[0], "%s.%s: not a user or group name or id", fld, x[0])
		}
	}
	if mode != "" && !recipeModeRE.MatchString(mode) {
		return Invalid(fld+".mode", "%s.mode: must be three octal digits such as 0644 (no set-id or sticky bit)", fld)
	}
	return nil
}

func recipeDir(fld string, d api.RecipeDir) error {
	fld += ".dir"
	if err := AbsPath(fld+".path", d.Path); err != nil {
		return err
	}
	if d.Path == "/" || RecipePathDenied(d.Path) {
		return Invalid(fld+".path", "%s.path: the path belongs to the base system: put local configuration below /usr/local/etc", fld)
	}
	if err := recipeOwnerMode(fld, d.Owner, d.Group, d.Mode); err != nil {
		return err
	}
	// the owner of a directory can always enter it and write: the extractor never leaves less than 0700
	if d.Mode != "" && d.Mode[len(d.Mode)-3] != '7' {
		return Invalid(fld+".mode", "%s.mode: the owner of a directory has all rights (7xx, such as 0755 or 0750)", fld)
	}
	return nil
}

func recipeSysrc(fld string, st api.RecipeStep) error {
	fld += ".sysrc"
	if len(st.Sysrc) == 0 || len(st.Sysrc) > 100 {
		return Invalid(fld, "%s: between 1 and 100 variables", fld)
	}
	if st.In != "" && !contains(RecipeSysrcFiles, st.In) {
		return Invalid(fld, "%s: in is rc.conf or periodic.conf", fld)
	}
	for k, v := range st.Sysrc {
		if !recipeVarRE.MatchString(k) {
			return Invalid(fld, "%s: a key is not a variable name", fld)
		}
		if len(v) > 1024 || strings.ContainsAny(v, "\n\r\x00") {
			return Invalid(fld, "%s: the value of %s is one line of text", fld, k)
		}
	}
	return nil
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func recipeService(fld string, s api.RecipeService) error {
	fld += ".service"
	if !recipeSvcRE.MatchString(s.Name) {
		return Invalid(fld+".name", "%s.name: not a service name", fld)
	}
	if s.Enabled == nil && s.Running == nil {
		return Invalid(fld, "%s: set enabled, running or both", fld)
	}
	return nil
}

func recipeArgv(fld string, argv []string) error {
	if len(argv) == 0 || argv[0] == "" || len(argv) > 64 {
		return Invalid(fld, "%s: a command is a non-empty argv of at most 64 words", fld)
	}
	for _, a := range argv {
		if strings.ContainsRune(a, 0) || len(a) > 4096 {
			return Invalid(fld, "%s: a command word is not valid", fld)
		}
	}
	return nil
}

func recipeRun(fld string, st api.RecipeStep) error {
	if err := recipeArgv(fld+".run", st.Run); err != nil {
		return err
	}
	if st.Unless != nil {
		if err := recipeArgv(fld+".unless", st.Unless); err != nil {
			return err
		}
	}
	if st.Creates != "" {
		return AbsPath(fld+".creates", st.Creates)
	}
	return nil
}

// recipeRequirements checks the requirements: a check is a command (argv), a message is one non-empty line. Whether the
// command succeeds is known only inside the jail.
func recipeRequirements(l []api.RecipeRequirement) error {
	if len(l) > RecipeMaxRequirements {
		return Invalid("requirements", "a recipe has at most %d requirements", RecipeMaxRequirements)
	}
	for i, q := range l {
		fld := fmt.Sprintf("requirements[%d]", i)
		if err := recipeArgv(fld+".check", q.Check); err != nil {
			return err
		}
		if strings.TrimSpace(q.Message) == "" || len(q.Message) > 500 || !utf8.ValidString(q.Message) || strings.ContainsAny(q.Message, "\n\r\x00") {
			return Invalid(fld+".message", "%s.message: one line of text of at most 500 bytes that tells what is missing", fld)
		}
	}
	return nil
}
