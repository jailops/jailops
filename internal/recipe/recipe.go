// Package recipe reads a recipe the way a client sends it: the YAML file with its variables filled in and the files
// beside it read in, as the finished api.Recipe the daemon takes. It is pure: no network, no daemon, no host path
// other than the directory of the recipe. The command line, jailops-sync and the checks of the Recipe Book use it, and
// the console renders the same way in the browser (testdata/render_vectors.json is the shared test).
//
// Variables (docs/provisioning.md):
//
//   - `vars:` at the top declares them: NAME: {default, required, secret, split}. A name is [A-Za-z_][A-Za-z0-9_]*.
//
//   - `${name}` is replaced in every string value of a step, except the body of a file. The body of a file (content or
//     the text of source) is replaced only when the step says `template: true`; any other file goes byte for byte.
//
//   - `$${` is a literal `${`; `$$` that is not followed by `{` stays as it is. A single pass: a value is never
//     searched for variables again.
//
//   - The value comes from the default, then --vars, then --set or --set-file. A name in a --vars file that the recipe
//     does not declare is ignored (one file serves many recipes); in --set it is an error, so is a required variable
//     without a value and a declared variable the recipe does not use.
//
//   - split: true cuts the value at white space where the recipe takes a list: a list word that is exactly ${name}
//     (in package, run or unless) becomes one word per piece, and so does a package value that is exactly ${name}.
//     Anywhere else the variable is one string.
//
//   - secret: true has no default and is given only by --set-file (or SetFile of a caller that holds it in memory),
//     never on a command line. It may be used only in the content of a file step with template: true: not in an
//     argument, a path, a name or a source.
//
//   - `requirements:` (before `steps:`) lists what the jail must have: each item is `check: [argv...]`, a command that
//     runs inside the jail (exit status 0 means met), and `message:`, shown when it is not. ${name} is replaced in check
//     (with split as in run) and in message; a secret variable may not be used in a requirement.
//
// An error names the step and the field (steps[2].run[1]) and the variable, never a value or the content of a file.
package recipe

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"jailops.net/jailops/internal/validate"
	"jailops.net/jailops/sdk/api"
)

// RecipeFile is the name of the recipe in a directory.
const RecipeFile = "recipe.yaml"

var nameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Values are the values a caller gives the variables. A caller holds them already read: Set is --set, SetFile is
// --set-file (the text of the file, see ReadValueFile; a caller that keeps a secret in memory gives it here), Vars is
// a --vars file (see ParseVarsFile).
type Values struct {
	Set     map[string]string
	SetFile map[string]string
	Vars    map[string]string
}

// VarDecl is one declaration of the vars: section, in the order of the file. A form for the variables is made from
// the list. Default is nil when the variable has none.
type VarDecl struct {
	Name     string
	Default  *string
	Required bool
	Secret   bool
	Split    bool
}

// Loaded is a recipe ready to send.
type Loaded struct {
	// Recipe has the variables replaced, the files beside the recipe read into Content and vars: removed.
	Recipe api.Recipe
	// Decls are the declared variables.
	Decls []VarDecl
	// Sensitive is true when a secret variable went into the recipe: the request must say so (api.ApplyRequest.Sensitive).
	Sensitive bool
}

// Request is the body of an apply for the loaded recipe.
func (l *Loaded) Request(dryRun bool) api.ApplyRequest {
	return api.ApplyRequest{Recipe: l.Recipe, DryRun: dryRun, Sensitive: l.Sensitive}
}

// Validate is the check of the daemon (validate.Recipe) on the rendered recipe. Its messages name the step and the
// field and carry no value.
func (l *Loaded) Validate() error { return validate.Recipe(l.Recipe) }

// LoadPath loads the recipe at the path p of this host: a file, or a directory that holds recipe.yaml. Files beside
// the recipe are read through a root held on its directory: a symbolic link that leads out of it is refused.
func LoadPath(p string, v Values) (*Loaded, error) {
	dir, name := p, RecipeFile
	if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
		dir, name = filepath.Split(p)
		if dir == "" {
			dir = "."
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return Load(root.FS(), name, v)
}

// LoadBytes renders a recipe that has no files beside it (read from standard input): a source is an error.
func LoadBytes(data []byte, v Values) (*Loaded, error) {
	return render(data, nil, ".", v)
}

// Load reads the recipe file (a slash path in fsys; a directory means file/recipe.yaml) and the files beside it. fsys is
// the confinement: it must not lead out of the directory of the recipe (LoadPath passes an os.Root for that).
func Load(fsys fs.FS, file string, v Values) (*Loaded, error) {
	file = path.Clean(file)
	if fi, err := fs.Stat(fsys, file); err == nil && fi.IsDir() {
		file = path.Join(file, RecipeFile)
	}
	data, err := readLimited(fsys, file)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return render(data, fsys, path.Dir(file), v)
}

func readLimited(fsys fs.FS, name string) ([]byte, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > validate.RecipeFileMax {
		return nil, fmt.Errorf("not a regular file of at most %d MiB", validate.RecipeFileMax>>20)
	}
	return io.ReadAll(io.LimitReader(f, validate.RecipeFileMax+1))
}

// ReadValueFile reads the file of a --set-file: its text, without one trailing line break.
func ReadValueFile(p string) (string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	if len(b) > validate.RecipeFileMax {
		return "", fmt.Errorf("%s is larger than %d MiB", p, validate.RecipeFileMax>>20)
	}
	if !utf8.Valid(b) {
		return "", fmt.Errorf("%s is not text", p)
	}
	s := string(b)
	s = strings.TrimSuffix(s, "\n")
	return strings.TrimSuffix(s, "\r"), nil
}

// ParseVarsFile reads a --vars file: lines KEY=VALUE, empty lines and lines that start with # are skipped, a value may
// be in single or double quotes (no escapes), and a comment may follow after white space and #. `export KEY=VALUE` is
// read as KEY=VALUE. A value is one line.
func ParseVarsFile(b []byte) (map[string]string, error) {
	out := map[string]string{}
	for i, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || line[0] == '#' {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, val, ok := strings.Cut(line, "=")
		k = strings.TrimSpace(k)
		if !ok || !nameRE.MatchString(k) {
			return nil, fmt.Errorf("line %d: want NAME=VALUE", i+1)
		}
		val = strings.TrimLeft(val, " \t")
		if val != "" && (val[0] == '"' || val[0] == '\'') {
			end := strings.IndexByte(val[1:], val[0])
			if end < 0 {
				return nil, fmt.Errorf("line %d: %s has an opening quote and no closing one", i+1, k)
			}
			if rest := strings.TrimSpace(val[end+2:]); rest != "" && rest[0] != '#' {
				return nil, fmt.Errorf("line %d: %s has text after the closing quote", i+1, k)
			}
			val = val[1 : end+1]
		} else {
			if j := strings.IndexAny(val, " \t"); j >= 0 {
				if rest := strings.TrimLeft(val[j:], " \t"); rest != "" && rest[0] == '#' {
					val = val[:j]
				}
			}
			val = strings.TrimRight(val, " \t")
		}
		out[k] = val
	}
	return out, nil
}

// ---- rendering ----

type renderer struct {
	vals    map[string]string
	decls   map[string]*VarDecl
	used    map[string]bool
	secrets bool // a secret went into the recipe
}

func render(data []byte, fsys fs.FS, dir string, v Values) (*Loaded, error) {
	root, others, err := parseDoc(data)
	if err != nil {
		return nil, err
	}
	r := &renderer{vals: map[string]string{}, decls: map[string]*VarDecl{}, used: map[string]bool{}}
	var decls []VarDecl
	if root.varsNode != nil {
		if decls, err = parseDecls(root.varsNode); err != nil {
			return nil, err
		}
	}
	if err := r.resolve(decls, v); err != nil {
		return nil, err
	}
	reqs, err := r.requirements(root.reqs)
	if err != nil {
		return nil, err
	}
	var steps []any
	if root.steps != nil {
		if err := root.steps.Decode(&steps); err != nil {
			return nil, fmt.Errorf("steps: a list of steps")
		}
	}
	for i, s := range steps {
		m, ok := s.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("steps[%d]: a step is a map with one of package, file, dir, sysrc, service, run", i)
		}
		out, err := r.step(fmt.Sprintf("steps[%d]", i), m, fsys, dir)
		if err != nil {
			return nil, err
		}
		steps[i] = out
	}
	for _, d := range decls {
		if !r.used[d.Name] {
			return nil, fmt.Errorf("vars.%s: declared and never used in the recipe", d.Name)
		}
	}
	doc := others
	if len(reqs) > 0 {
		doc["requirements"] = reqs
	}
	doc["steps"] = steps
	if steps == nil {
		doc["steps"] = []any{}
	}
	rec, err := decodeRecipe(doc)
	if err != nil {
		return nil, err
	}
	return &Loaded{Recipe: rec, Decls: decls, Sensitive: r.secrets}, nil
}

type docNodes struct{ varsNode, reqs, steps *yaml.Node }

// parseDoc splits the document into the vars node, the steps node and the other top-level keys (decoded, for the strict
// check of the recipe at the end).
func parseDoc(data []byte) (docNodes, map[string]any, error) {
	var d docNodes
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var n yaml.Node
	if err := dec.Decode(&n); err != nil {
		if errors.Is(err, io.EOF) {
			return d, nil, errors.New("the recipe is empty")
		}
		return d, nil, fmt.Errorf("invalid YAML: %v", err)
	}
	var second yaml.Node
	if err := dec.Decode(&second); err == nil {
		return d, nil, errors.New("a recipe is one document")
	}
	if len(n.Content) != 1 || n.Content[0].Kind != yaml.MappingNode {
		return d, nil, errors.New("a recipe is a map with steps:")
	}
	others := map[string]any{}
	m := n.Content[0]
	for i := 0; i+1 < len(m.Content); i += 2 {
		k, val := m.Content[i], m.Content[i+1]
		switch k.Value {
		case "vars":
			d.varsNode = val
		case "requirements":
			d.reqs = val
		case "steps":
			d.steps = val
		default:
			var x any
			if err := val.Decode(&x); err != nil {
				return d, nil, fmt.Errorf("%s: not usable", k.Value)
			}
			others[k.Value] = x
		}
	}
	return d, others, nil
}

func parseDecls(n *yaml.Node) ([]VarDecl, error) {
	if n.Kind == yaml.ScalarNode && n.ShortTag() == "!!null" {
		return nil, nil
	}
	if n.Kind != yaml.MappingNode {
		return nil, errors.New("vars: a map of NAME: {default, required, secret, split}")
	}
	var out []VarDecl
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		name, body := n.Content[i].Value, n.Content[i+1]
		where := "vars." + name
		if !nameRE.MatchString(name) {
			return nil, fmt.Errorf("vars: %q is not a variable name (letters, digits and _, not starting with a digit)", name)
		}
		if seen[name] {
			return nil, fmt.Errorf("%s: declared twice", where)
		}
		seen[name] = true
		d := VarDecl{Name: name}
		switch {
		case body.Kind == yaml.ScalarNode && body.ShortTag() == "!!null":
		case body.Kind != yaml.MappingNode:
			return nil, fmt.Errorf("%s: a map with default, required, secret and split", where)
		default:
			for j := 0; j+1 < len(body.Content); j += 2 {
				key, val := body.Content[j].Value, body.Content[j+1]
				flag := func(to *bool) error {
					if val.Kind != yaml.ScalarNode || val.ShortTag() != "!!bool" {
						return fmt.Errorf("%s.%s: true or false", where, key)
					}
					return val.Decode(to)
				}
				var err error
				switch key {
				case "default":
					switch {
					case val.Kind != yaml.ScalarNode:
						err = fmt.Errorf("%s.default: one value", where)
					case val.ShortTag() != "!!null":
						// the text as written: 22 is "22", true is "true", "" is the empty string
						s := val.Value
						d.Default = &s
					}
				case "required":
					err = flag(&d.Required)
				case "secret":
					err = flag(&d.Secret)
				case "split":
					err = flag(&d.Split)
				default:
					err = fmt.Errorf("%s: unknown key %q (default, required, secret, split)", where, key)
				}
				if err != nil {
					return nil, err
				}
			}
		}
		switch {
		case d.Secret && d.Default != nil:
			return nil, fmt.Errorf("%s: a secret has no default", where)
		case d.Secret && d.Split:
			return nil, fmt.Errorf("%s: a secret is not split", where)
		case d.Required && d.Default != nil:
			return nil, fmt.Errorf("%s: a variable is required or has a default, not both", where)
		}
		out = append(out, d)
	}
	return out, nil
}

// resolve gives every declared variable its value: default, then Vars, then Set and SetFile.
func (r *renderer) resolve(decls []VarDecl, v Values) error {
	for i := range decls {
		r.decls[decls[i].Name] = &decls[i]
	}
	for _, in := range []struct {
		flag string
		m    map[string]string
	}{{"--set", v.Set}, {"--set-file", v.SetFile}} {
		for k := range in.m {
			if r.decls[k] == nil {
				return fmt.Errorf("%s %s: the recipe declares no such variable", in.flag, k)
			}
		}
	}
	for _, d := range decls {
		_, inSet := v.Set[d.Name]
		_, inFile := v.SetFile[d.Name]
		var val string
		var have bool
		switch {
		case inSet && inFile:
			return fmt.Errorf("%s: given with --set and --set-file", d.Name)
		case inSet:
			if d.Secret {
				return fmt.Errorf("%s is secret: a value on the command line is kept in the shell history and shown by ps; give it with --set-file", d.Name)
			}
			val, have = v.Set[d.Name], true
			if strings.ContainsAny(val, "\r\n") {
				return fmt.Errorf("%s: a value of several lines is given with --set-file", d.Name)
			}
		case inFile:
			val, have = v.SetFile[d.Name], true
		default:
			if x, ok := v.Vars[d.Name]; ok {
				if d.Secret {
					return fmt.Errorf("%s is secret: give it with --set-file, not in a --vars file", d.Name)
				}
				if strings.ContainsAny(x, "\r\n") {
					return fmt.Errorf("%s: a value of several lines is given with --set-file", d.Name)
				}
				val, have = x, true
			}
		}
		if !have {
			switch {
			case d.Required && d.Secret:
				return fmt.Errorf("%s is required: give it with --set-file %s=FILE", d.Name, d.Name)
			case d.Required:
				return fmt.Errorf("%s is required: give it with --set %s=VALUE or in a --vars file", d.Name, d.Name)
			case d.Default != nil:
				// a default may use the variables declared before it
				var err error
				if val, err = r.expand("vars."+d.Name+".default", *d.Default, false); err != nil {
					return err
				}
			}
		}
		r.vals[d.Name] = val
	}
	return nil
}

// expand replaces the ${name} of s. where names the place for an error; secrets says whether a secret may go in.
func (r *renderer) expand(where, s string, secrets bool) (string, error) {
	if !strings.Contains(s, "$") {
		return s, nil
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		switch {
		case strings.HasPrefix(s[i:], "$${"):
			b.WriteString("${")
			i += 3
		case strings.HasPrefix(s[i:], "${"):
			end := strings.IndexByte(s[i:], '}')
			name := ""
			if end > 0 {
				name = s[i+2 : i+end]
			}
			at := where
			if strings.Contains(s[:i], "\n") {
				at = fmt.Sprintf("%s, line %d", where, 1+strings.Count(s[:i], "\n"))
			}
			if end < 0 || !nameRE.MatchString(name) {
				return "", fmt.Errorf("%s: a ${ must hold a variable name and end with } (write $${ for a literal ${)", at)
			}
			d := r.decls[name]
			if d == nil {
				return "", fmt.Errorf("%s: ${%s} is not declared under vars: (write $${ for a literal ${)", at, name)
			}
			if d.Secret && !secrets {
				return "", fmt.Errorf("%s: %s is secret and may be used only in the content of a file step with template: true", at, name)
			}
			r.used[name] = true
			if d.Secret {
				r.secrets = true
			}
			b.WriteString(r.vals[name])
			i += end + 1
		default:
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String(), nil
}

// wholeVar returns the name when s is exactly ${name}.
func wholeVar(s string) (string, bool) {
	if len(s) > 3 && s[:2] == "${" && s[len(s)-1] == '}' && nameRE.MatchString(s[2:len(s)-1]) {
		return s[2 : len(s)-1], true
	}
	return "", false
}

// list expands the words of a list field: a word that is exactly the ${name} of a split variable is several words.
func (r *renderer) list(where string, in []any) ([]any, error) {
	out := make([]any, 0, len(in))
	for i, e := range in {
		w := fmt.Sprintf("%s[%d]", where, i)
		s, ok := e.(string)
		if !ok {
			out = append(out, e)
			continue
		}
		if name, ok := wholeVar(s); ok && r.decls[name] != nil && r.decls[name].Split {
			if _, err := r.expand(w, s, false); err != nil { // marks the use, refuses a secret
				return nil, err
			}
			for _, f := range strings.Fields(r.vals[name]) {
				out = append(out, f)
			}
			continue
		}
		x, err := r.expand(w, s, false)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, nil
}

// value expands every string below v (not the keys of a map).
func (r *renderer) value(where string, v any) (any, error) {
	switch x := v.(type) {
	case string:
		return r.expand(where, x, false)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			var err error
			if out[i], err = r.value(fmt.Sprintf("%s[%d]", where, i), e); err != nil {
				return nil, err
			}
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			var err error
			if out[k], err = r.value(where+"."+k, e); err != nil {
				return nil, err
			}
		}
		return out, nil
	}
	return v, nil
}

// requirements renders the requirements: check as a word list, message as a string. A key that is not one of the two is
// an error here, so that it names the requirement.
func (r *renderer) requirements(n *yaml.Node) ([]any, error) {
	if n == nil || n.Kind == yaml.ScalarNode && n.ShortTag() == "!!null" {
		return nil, nil
	}
	var in []any
	if n.Kind != yaml.SequenceNode || n.Decode(&in) != nil {
		return nil, errors.New("requirements: a list of requirements, each with check and message")
	}
	out := make([]any, 0, len(in))
	for i, e := range in {
		where := fmt.Sprintf("requirements[%d]", i)
		m, ok := e.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s: a requirement is a map with check and message", where)
		}
		res := make(map[string]any, len(m))
		for k, v := range m {
			w := where + "." + k
			var err error
			switch k {
			case "check":
				if l, ok := v.([]any); ok {
					res[k], err = r.list(w, l)
				} else {
					res[k] = v // a wrong type is said by decodeRecipe
				}
			case "message":
				res[k], err = r.value(w, v)
			default:
				return nil, fmt.Errorf("%s: unknown field %q (check, message)", where, k)
			}
			if err != nil {
				return nil, err
			}
		}
		out = append(out, res)
	}
	return out, nil
}

func (r *renderer) step(where string, m map[string]any, fsys fs.FS, dir string) (map[string]any, error) {
	out := make(map[string]any, len(m))
	for k, v := range m {
		w := where + "." + k
		var err error
		switch k {
		case "file":
			fm, ok := v.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%s: a map with path, source or content, mode, owner, group", w)
			}
			out[k], err = r.file(w, fm, fsys, dir)
		case "package", "run", "unless":
			switch x := v.(type) {
			case []any:
				out[k], err = r.list(w, x)
			case string:
				// package: ${pkgs} (a split variable) is the whole list
				if name, ok := wholeVar(x); ok && k == "package" && r.decls[name] != nil && r.decls[name].Split {
					out[k], err = r.list(w, []any{x})
				} else {
					out[k], err = r.value(w, v)
				}
			default:
				out[k], err = r.value(w, v)
			}
		default:
			out[k], err = r.value(w, v)
		}
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// file reads the body of a file step: the text of content or of the source beside the recipe, with the variables put
// in when template is true.
func (r *renderer) file(where string, f map[string]any, fsys fs.FS, dir string) (map[string]any, error) {
	tmpl := false
	if t, ok := f["template"]; ok {
		b, isBool := t.(bool)
		if !isBool {
			return nil, fmt.Errorf("%s.template: true or false", where)
		}
		tmpl = b
	}
	out := make(map[string]any, len(f))
	for k, v := range f {
		if k == "template" || k == "content" {
			continue
		}
		var err error
		if out[k], err = r.value(where+"."+k, v); err != nil {
			return nil, err
		}
	}
	content, hasContent := f["content"]
	src, hasSource := out["source"].(string)
	if _, set := f["source"]; set && !hasSource {
		return nil, fmt.Errorf("%s.source: a file name", where)
	}
	if hasContent && hasSource {
		return nil, fmt.Errorf("%s: give a source or content, not both", where)
	}
	var body string
	switch {
	case hasSource:
		b, err := readSource(fsys, dir, src, where+".source")
		if err != nil {
			return nil, err
		}
		body = b
		delete(out, "source")
	case hasContent:
		s, ok := content.(string)
		if !ok {
			return nil, fmt.Errorf("%s.content: text; put quotes around it", where)
		}
		body = s
	default:
		if tmpl {
			return nil, fmt.Errorf("%s: template: true needs a source or content", where)
		}
		return out, nil // validate says that a file needs a source or content
	}
	if tmpl {
		var err error
		if body, err = r.expand(where+" (template)", body, true); err != nil {
			return nil, err
		}
	}
	out["content"] = body
	return out, nil
}

// readSource reads the file rel (below dir of fsys) of a recipe: text, at most validate.RecipeFileMax.
func readSource(fsys fs.FS, dir, rel, where string) (string, error) {
	if fsys == nil {
		return "", fmt.Errorf("%s: a recipe on standard input has no files beside it: use content", where)
	}
	if path.IsAbs(rel) || path.Clean(rel) != rel || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("%s: %s must be a path below the directory of the recipe", where, rel)
	}
	p := path.Join(dir, rel)
	data, err := readLimited(fsys, p)
	if err != nil {
		return "", fmt.Errorf("%s: %s is not a regular file of at most %d MiB below the directory of the recipe (a link that leads out of it is refused)", where, rel, validate.RecipeFileMax>>20)
	}
	if !utf8.Valid(data) {
		return "", fmt.Errorf("%s: %s is not text; copy other files with 'jailops cp'", where, rel)
	}
	return string(data), nil
}

// decodeRecipe turns the rendered tree into the typed recipe and refuses what it does not know. The text of a type
// error names the field, never the value.
func decodeRecipe(doc map[string]any) (api.Recipe, error) {
	var rec api.Recipe
	b, err := json.Marshal(doc)
	if err != nil {
		return rec, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rec); err != nil {
		var te *json.UnmarshalTypeError
		if errors.As(err, &te) {
			return rec, typeError(te)
		}
		return rec, errors.New(strings.ReplaceAll(err.Error(), "json: unknown field", "unknown field"))
	}
	return rec, nil
}

var segNumRE = regexp.MustCompile(`^[0-9]+$`)

// typeError is the message for a value of the wrong type: YAML read a word such as false, yes, 5 or 0644 as a boolean or
// a number where the recipe wants text.
func typeError(te *json.UnmarshalTypeError) error {
	var where strings.Builder
	for _, seg := range strings.Split(te.Field, ".") {
		switch {
		case segNumRE.MatchString(seg):
			where.WriteString("[" + seg + "]")
		case where.Len() == 0 || seg == "Recipe":
			if seg != "Recipe" {
				where.WriteString(seg)
			}
		default:
			where.WriteString("." + seg)
		}
	}
	got := strings.Fields(te.Value + " ")[0]
	switch got {
	case "bool":
		got = "a true/false value"
	case "number":
		got = "a number"
	}
	if te.Type != nil && te.Type.Kind().String() == "string" {
		return fmt.Errorf("%s: YAML reads this as %s, not as text: put quotes around it (\"false\", \"0644\", \"5\")", where.String(), got)
	}
	return fmt.Errorf("%s: wrong type of value (%s)", where.String(), got)
}
