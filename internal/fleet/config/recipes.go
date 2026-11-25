package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"jailops.net/jailops/internal/fleet/model"
	"jailops.net/jailops/internal/recipe"
	"jailops.net/jailops/sdk/api"
)

// DefaultDriftInterval is how often sync looks inside a running jail again to see whether a recipe still holds.
const DefaultDriftInterval = time.Hour

// DriftInterval is drift_interval of fleet.yaml, or the default.
func (f *Fleet) DriftInterval() (time.Duration, error) {
	if f.Meta.DriftInterval == "" {
		return DefaultDriftInterval, nil
	}
	d, err := time.ParseDuration(f.Meta.DriftInterval)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%q is not a positive duration such as 30m or 2h", f.Meta.DriftInterval)
	}
	return d, nil
}

// RecipeLabelPrefix starts the label on a jail that holds the hash of one recipe applied successfully.
const RecipeLabelPrefix = SyncLabelPrefix + "recipe."

// RecipeLabel is the label that records the recipe name on a jail.
func RecipeLabel(name string) string { return RecipeLabelPrefix + name }

// RecipeSource is one recipe of a jail: a directory of the repository and the values the fleet gives its variables.
// Secrets are references into the secret store, never values.
type RecipeSource struct {
	// Name is the last element of Path: the name of the recipe in the jail and in its label.
	Name, Path string
	// Vars are the plain values of the entry, Secrets the references of its secret variables, JailVars the values
	// of the jail that every recipe of it receives for the names it declares.
	Vars, Secrets, JailVars map[string]string
	// Decls are the variables the recipe declares.
	Decls []recipe.VarDecl

	root string // the fleet repository
}

// SecretResolver gives the secrets of a recipe to a render. Value is the secret itself (it needs the key), Stamp is a
// fingerprint of what is stored (it needs no key and reveals nothing): the hash of a recipe holds stamps, never values.
type SecretResolver interface {
	Value(ref string) (string, error)
	Stamp(ref string) (string, error)
}

// Resolved is a recipe rendered for one run.
type Resolved struct {
	// Recipe is the rendered recipe, secrets in it. It is empty when ValueErr is set.
	Recipe    api.Recipe
	Sensitive bool
	// Hash identifies the recipe: its rendering with the stamps of the secrets in place of their values, the files
	// beside it and the variables. A label may carry it: it tells nothing about a secret.
	Hash string
	// ValueErr is why the secrets could not be read (no key). The hash is still known.
	ValueErr error
}

// Request is the body of an apply of the recipe.
func (r Resolved) Request(dryRun bool) api.ApplyRequest {
	return api.ApplyRequest{Recipe: r.Recipe, DryRun: dryRun, Sensitive: r.Sensitive}
}

// Resolve reads the recipe directory and renders it with res.
func (s RecipeSource) Resolve(res SecretResolver) (Resolved, error) {
	vals := map[string]string{}
	stamps := map[string]string{}
	var valErr error
	for name, ref := range s.Secrets {
		st, err := res.Stamp(ref)
		if err != nil {
			return Resolved{}, fmt.Errorf("variable %s: %w", name, err)
		}
		stamps[name] = st
		v, err := res.Value(ref)
		if err != nil {
			valErr = fmt.Errorf("variable %s: %w", name, err)
			continue
		}
		vals[name] = v
	}
	h, err := s.load(stamps)
	if err != nil {
		return Resolved{}, err
	}
	out := Resolved{Sensitive: h.Sensitive, Hash: hashRecipe(h.Recipe, h.Sensitive), ValueErr: valErr}
	if valErr != nil {
		return out, nil
	}
	if len(s.Secrets) == 0 {
		out.Recipe = h.Recipe
		return out, nil
	}
	real, err := s.load(vals)
	if err != nil {
		return Resolved{}, err
	}
	out.Recipe = real.Recipe
	return out, nil
}

func (s RecipeSource) load(secret map[string]string) (*recipe.Loaded, error) {
	root, err := os.OpenRoot(s.root)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	dir, err := root.OpenRoot(s.Path)
	if err != nil {
		return nil, noRecipeDir(s.Path, err)
	}
	defer dir.Close()
	files := map[string]string{}
	for k, v := range s.Vars {
		files[k] = v
	}
	for k, v := range secret {
		files[k] = v
	}
	l, err := recipe.Load(dir.FS(), recipe.RecipeFile, recipe.Values{SetFile: files, Vars: s.JailVars})
	if err != nil {
		return nil, noRecipeDir(s.Path, fleetError(err))
	}
	return l, nil
}

func noRecipeDir(p string, err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("recipe %s: %w (a recipe of the Recipe Book is a git submodule: git submodule update --init)", p, err)
	}
	return fmt.Errorf("recipe %s: %w", p, err)
}

// fleetError says what the error of a render means in a fleet file, where there are no flags.
func fleetError(err error) error {
	msg := err.Error()
	for _, r := range []struct{ re, to string }{
		{`(\w+) is secret: [^\n]*`, `$1 is secret: give it under secrets: of the recipe as a reference into the secret store, never as a value`},
		{`(\w+) is required: give it with --set-file \S+`, `$1 is required: give it under secrets: of the recipe`},
		{`(\w+) is required: give it with --set \S+ or in a --vars file`, `$1 is required: give it under vars: of the recipe or of the jail`},
		{`--set-file (\w+)`, `$1`},
		{`--set (\w+)`, `$1`},
	} {
		msg = regexp.MustCompile(r.re).ReplaceAllString(msg, r.to)
	}
	return errors.New(msg)
}

func hashRecipe(r api.Recipe, sensitive bool) string {
	b, _ := json.Marshal(struct {
		Recipe    api.Recipe
		Sensitive bool
	}{r, sensitive})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:16]
}

var recipeNameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]{0,48}[a-z0-9])?$`)

// check renders the recipe once with a stand-in for each secret, so that a mistake of the repository shows offline:
// the files, the variables and the check of the daemon (validate.Recipe). It keeps the declarations.
func (s *RecipeSource) check() error {
	stand := map[string]string{}
	for k := range s.Secrets {
		stand[k] = "stand-in"
	}
	l, err := s.load(stand)
	if err != nil {
		return err
	}
	if err := l.Validate(); err != nil {
		return noRecipeDir(s.Path, err)
	}
	s.Decls = l.Decls
	secret := map[string]bool{}
	for _, d := range l.Decls {
		secret[d.Name] = d.Secret
	}
	for _, k := range sortedKeys(s.Vars) {
		if secret[k] {
			return fmt.Errorf("recipe %s: %s is secret: give it under secrets: as a reference into the secret store, never as a value", s.Path, k)
		}
	}
	for _, k := range sortedKeys(s.Secrets) {
		if !secret[k] {
			return fmt.Errorf("recipe %s: %s is not a secret variable of the recipe: give it under vars:", s.Path, k)
		}
	}
	return nil
}

// loadRecipes reads the recipe directories a jail lists. A path stays inside recipes/ of the repository (a link out of a
// recipe directory is refused) and a name occurs once per jail, whether a profile or the jail lists it.
func (f *Fleet) loadRecipes(refs []model.RecipeRef, vars map[string]string) ([]RecipeSource, error) {
	var out []RecipeSource
	seen := map[string]bool{}
	declared := map[string]bool{}
	for _, r := range refs {
		if !fs.ValidPath(r.Path) || path.Clean(r.Path) != r.Path || !strings.HasPrefix(r.Path, "recipes/") {
			return nil, fmt.Errorf("recipe %q: a directory inside recipes/ of the repository, such as recipes/web", r.Path)
		}
		name := path.Base(r.Path)
		if !recipeNameRE.MatchString(name) || api.ValidLabelKey(RecipeLabel(name)) != nil {
			return nil, fmt.Errorf("recipe %q: the directory name %q becomes a label: lowercase letters, digits, '.', '-' and '_'", r.Path, name)
		}
		if seen[name] {
			return nil, fmt.Errorf("recipe %q: two recipes of a jail are named %q", r.Path, name)
		}
		seen[name] = true
		for k, ref := range r.Secrets {
			if _, dup := r.Vars[k]; dup {
				return nil, fmt.Errorf("recipe %s: %s is given under vars and under secrets", r.Path, k)
			}
			if err := checkSecretRef(ref); err != nil {
				return nil, fmt.Errorf("recipe %s: secrets.%s: %w", r.Path, k, err)
			}
		}
		s := RecipeSource{Name: name, Path: r.Path, Vars: r.Vars, Secrets: r.Secrets, JailVars: vars, root: f.Path}
		if err := s.check(); err != nil {
			return nil, err
		}
		for _, d := range s.Decls {
			declared[d.Name] = true
		}
		out = append(out, s)
	}
	for _, k := range sortedKeys(vars) {
		if !declared[k] {
			return nil, fmt.Errorf("vars.%s: no recipe of the jail declares this variable", k)
		}
	}
	return out, nil
}

func recipeRefs(rs []RecipeSource) []model.RecipeRef {
	out := make([]model.RecipeRef, len(rs))
	for i, r := range rs {
		out[i] = model.RecipeRef{Path: r.Path, Vars: r.Vars, Secrets: r.Secrets}
	}
	return out
}

// NeedsOrder returns the jails of one host that have recipes, in the order their recipes run: a jail after those it
// needs, otherwise as given (by project and name). It fails on a cycle.
func NeedsOrder(jails []*Object) ([]*Object, error) {
	byName := map[string]*Object{}
	for _, o := range jails {
		byName[o.Project+"/"+o.Name] = o
	}
	var out []*Object
	state := map[string]int{} // 1 visiting, 2 done
	var visit func(o *Object) error
	visit = func(o *Object) error {
		k := o.Project + "/" + o.Name
		switch state[k] {
		case 1:
			return fmt.Errorf("%s: needs make a cycle through %s", o.Origin, o.Name)
		case 2:
			return nil
		}
		state[k] = 1
		for _, n := range o.Needs {
			if d := byName[o.Project+"/"+n]; d != nil {
				if err := visit(d); err != nil {
					return err
				}
			}
		}
		state[k] = 2
		out = append(out, o)
		return nil
	}
	for _, o := range jails {
		if err := visit(o); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (f *Fleet) checkNeeds() []error {
	var errs []error
	jails := map[string]*Object{}
	for _, o := range f.Objects {
		if o.Kind == api.KindJail {
			jails[o.Project+"/"+o.Name] = o
		}
	}
	keys := make([]string, 0, len(jails))
	for k := range jails {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		o := jails[k]
		for _, n := range o.Needs {
			d := jails[o.Project+"/"+n]
			switch {
			case d == nil:
				errs = append(errs, fmt.Errorf("%s: needs %q: no such jail in project %s", o.Origin, n, o.Project))
			case len(d.Recipes) == 0:
				errs = append(errs, fmt.Errorf("%s: needs %q: that jail has no recipes", o.Origin, n))
			default:
				for _, h := range o.Hosts {
					if !slices.Contains(d.Hosts, h) {
						errs = append(errs, fmt.Errorf("%s: needs %q: that jail is not declared on host %s", o.Origin, n, h))
					}
				}
			}
		}
	}
	for _, h := range f.HostNames() {
		var js []*Object
		for _, o := range f.ObjectsFor(h) {
			if o.Kind == api.KindJail {
				js = append(js, o)
			}
		}
		if _, err := NeedsOrder(js); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}
