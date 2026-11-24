package service

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"jailops.net/jailops/internal/apps"
	"jailops.net/jailops/internal/render"
	"jailops.net/jailops/internal/validate"
	"jailops.net/jailops/sdk/api"
)

// Applying a recipe (docs/provisioning.md): the steps of a recipe run in order inside a running FreeBSD jail. Every step
// reads the state it is about and changes only what differs, so a second apply changes nothing. The steps never stop or
// restart the jail, never change its spec, and a failed step stops the run; the steps before it stay, and running the
// recipe again is the way to finish. A dry run reads the same state and changes nothing: it runs none of the recipe's
// commands.

var svcVarRE = regexp.MustCompile(`[^A-Za-z0-9_]`)

// applyRun is the state of one apply.
type applyRun struct {
	s    *Service
	key  string
	dry  bool
	root string
	res  api.ApplyResult
	// accounts are the lines of the jail's /etc/passwd and /etc/group, read when a file names an owner.
	passwd, group string
}

// stepOutcome is what a step did.
type stepOutcome struct {
	kind, name, status, detail string
}

// ApplyJail runs a recipe in a running FreeBSD jail as an operation.
func (s *Service) ApplyJail(ctx context.Context, name string, req api.ApplyRequest) (api.Operation, error) {
	if err := s.requireHost("applying a recipe"); err != nil {
		return api.Operation{}, err
	}
	if s.d.JailFiles == nil {
		return api.Operation{}, api.Errorf(api.CodeUnsupported, "this daemon has no way to read or write files of jails").WithHint("jailops-priv offers it; run jailopsd with the helper")
	}
	p, key, err := s.jailRef(ctx, name)
	if err != nil {
		return api.Operation{}, err
	}
	j, err := s.jailSnapshot(key)
	if err != nil {
		return api.Operation{}, err
	}
	if s.jailIsLinux(j) {
		return api.Operation{}, api.Errorf(api.CodeUnsupported, "jail %q is a Linux jail: a recipe is for FreeBSD jails", name).WithHint("use jailops exec and jailops cp")
	}
	if err := validate.Recipe(req.Recipe); err != nil {
		return api.Operation{}, err
	}
	return s.Submit(ctx, OpSpec{Action: api.OpJailApply, Description: "Apply a recipe to jail " + name, Locks: []string{jailLock(key)}, Project: p.Meta.Name,
		Resources: map[string][]string{"jails": {"/api/v1/jails/" + name}},
		Run: func(ctx context.Context, h *OpHandle) (any, error) {
			if !s.jailRunning(ctx, key) {
				return nil, api.Errorf(api.CodeBusy, "jail %q is not running; a recipe runs inside it", name).WithHint("start it first")
			}
			a := &applyRun{s: s, key: key, dry: req.DryRun, root: s.rootMount(key), res: api.ApplyResult{Jail: name, DryRun: req.DryRun, Steps: []api.ApplyStep{}}}
			if err := a.run(ctx, req.Recipe); err != nil {
				return nil, err
			}
			if !req.DryRun {
				s.emitIn(ctx, p.Meta.Name, "jail-applied", "/api/v1/jails/"+name)
			}
			return a.res, nil
		}})
}

// run runs the steps and stops at the first failure; the error carries the report so far and the output of the step.
func (a *applyRun) run(ctx context.Context, r api.Recipe) error {
	for i, st := range r.Steps {
		if err := ctx.Err(); err != nil {
			return err
		}
		o, out, err := a.step(ctx, st)
		if err != nil {
			a.res.Steps = append(a.res.Steps, api.ApplyStep{Kind: o.kind, Name: o.name, Status: api.StepFailed})
			msg := strings.Join(strings.Split(strings.TrimSpace(tailLines(out, 20)), "\n"), " / ")
			if msg == "" {
				msg = err.Error()
			}
			var done []string
			for _, d := range a.res.Steps[:len(a.res.Steps)-1] {
				done = append(done, d.Kind+" "+d.Name+" "+d.Status)
			}
			hint := "Nothing is undone: fix the recipe and apply it again."
			if len(done) > 0 {
				hint = "Before it: " + strings.Join(done, "; ") + ". " + hint
			}
			// one line of text: the client does not print a line break of an error
			return &publicError{ae: api.Errorf(api.CodeInternal, "step %d of %d failed (%s %s): %s", i+1, len(r.Steps), o.kind, o.name, msg).WithHint(hint), cause: err}
		}
		a.res.Steps = append(a.res.Steps, api.ApplyStep{Kind: o.kind, Name: o.name, Status: o.status, Detail: o.detail})
	}
	return nil
}

// tailLines is the last n lines of b.
func tailLines(b []byte, n int) string {
	l := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(l) > n {
		l = append([]string{"..."}, l[len(l)-n:]...)
	}
	return strings.Join(l, "\n")
}

// step runs one step. out is what a failed command printed.
func (a *applyRun) step(ctx context.Context, st api.RecipeStep) (o stepOutcome, out []byte, err error) {
	switch {
	case st.Package != nil:
		return a.packages(ctx, st.Package)
	case st.File != nil:
		return a.file(ctx, *st.File)
	case st.Sysrc != nil:
		return a.sysrc(ctx, st)
	case st.Service != nil:
		return a.service(ctx, *st.Service)
	}
	return a.command(ctx, st)
}

// inJail runs argv in the jail with the login PATH: the PATH of the daemon has no /usr/local.
func (a *applyRun) inJail(ctx context.Context, timeout time.Duration, argv ...string) ([]byte, error) {
	return a.s.runInJail(ctx, a.key, timeout, append([]string{"/usr/bin/env", "PATH=" + ShellPATH}, argv...)...)
}

func (a *applyRun) packages(ctx context.Context, names []string) (stepOutcome, []byte, error) {
	o := stepOutcome{kind: "package", name: strings.Join(names, " ")}
	// the host's pkg works in the jail (-j): a minimal jail needs no package tool of its own
	out, err := a.s.pkgExec(ctx, a.key, 2*time.Minute, "pkg", "query", "%n")
	if err != nil {
		return o, out, err
	}
	have := strings.Fields(string(out))
	var missing []string
	for _, n := range names {
		if !slices.Contains(have, n) && !slices.Contains(missing, n) {
			missing = append(missing, n)
		}
	}
	if len(missing) == 0 {
		o.status = api.StepUnchanged
		return o, nil, nil
	}
	o.detail = "install " + strings.Join(missing, " ")
	if a.dry {
		o.status = api.StepWouldChange
		return o, nil, nil
	}
	for len(missing) > 0 {
		n := min(len(missing), pkgUpgradeChunk)
		if out, err := a.s.pkgExec(ctx, a.key, 30*time.Minute, append([]string{"pkg", "install", "-y"}, missing[:n]...)...); err != nil {
			return o, out, err
		}
		missing = missing[n:]
	}
	o.status, o.detail = api.StepChanged, strings.Replace(o.detail, "install ", "installed ", 1)
	return o, nil, nil
}

// readRoot reads a file of the jail root through the host; a missing file is "" and false.
func (a *applyRun) jailText(ctx context.Context, rel string) (string, bool, error) {
	b, _, ok, err := a.readFile(ctx, rel)
	return string(b), ok, err
}

// readFile returns the content and the tar header (mode, owner) of the regular file at rel (no leading slash); ok is
// false when nothing is there. A path that is not a regular file is returned with its header and a nil body.
func (a *applyRun) readFile(ctx context.Context, rel string) ([]byte, *tar.Header, bool, error) {
	f := a.s.d.JailFiles
	st, err := f.JailStat(ctx, JailStatReq{Jail: a.key, Root: a.root, Path: rel})
	if err != nil {
		return nil, nil, false, err
	}
	if st.Type == api.FileTypeNone {
		return nil, nil, false, nil
	}
	if st.Type != api.FileTypeFile {
		return nil, &tar.Header{Typeflag: tar.TypeDir}, true, nil
	}
	if st.Size > validate.RecipeFileMax {
		return nil, &tar.Header{Typeflag: tar.TypeReg, Size: st.Size}, true, nil
	}
	var buf bytes.Buffer
	if _, err := f.JailGet(ctx, JailGetReq{Jail: a.key, Root: a.root, Path: rel, Preserve: true, MaxBytes: validate.RecipeFileMax + 1<<10}, &buf); err != nil {
		return nil, nil, false, err
	}
	tr := tar.NewReader(&buf)
	h, err := tr.Next()
	if err != nil {
		return nil, nil, false, err
	}
	b, err := io.ReadAll(tr)
	return b, h, true, err
}

// account resolves a user or group name of a file to its number through the jail's own /etc/passwd or /etc/group.
func (a *applyRun) account(ctx context.Context, name string, group bool) (int, error) {
	if n, err := strconv.Atoi(name); err == nil {
		return n, nil
	}
	file, col := &a.passwd, 2
	rel, what := "etc/passwd", "user"
	if group {
		file, col, rel, what = &a.group, 2, "etc/group", "group"
	}
	if *file == "" {
		t, _, err := a.jailText(ctx, rel)
		if err != nil {
			return 0, err
		}
		*file = t + "\n"
	}
	for _, l := range strings.Split(*file, "\n") {
		f := strings.Split(l, ":")
		if len(f) > col && f[0] == name {
			if n, err := strconv.Atoi(f[col]); err == nil {
				return n, nil
			}
		}
	}
	return 0, fmt.Errorf("the jail has no %s %q (a run step before this one can create it)", what, name)
}

func (a *applyRun) file(ctx context.Context, f api.RecipeFile) (stepOutcome, []byte, error) {
	o := stepOutcome{kind: "file", name: f.Path}
	want := []byte(*f.Content)
	modeText := f.Mode
	if modeText == "" {
		modeText = "0644"
	}
	m, _ := strconv.ParseUint(modeText, 8, 32)
	mode := int64(m & 0o777)
	rel := strings.TrimPrefix(f.Path, "/")
	cur, hdr, exists, err := a.readFile(ctx, rel)
	if err != nil {
		return o, nil, err
	}
	if exists && hdr.Typeflag == tar.TypeDir {
		return o, nil, fmt.Errorf("%s is a directory", f.Path)
	}
	// the owner is resolved in the jail; a dry run goes on when it does not exist yet (a run step may create it)
	uid, gid, ownerGap := 0, 0, ""
	if exists && f.Owner == "" {
		uid = hdr.Uid
	}
	if exists && f.Group == "" {
		gid = hdr.Gid
	}
	for _, x := range []struct {
		name  string
		group bool
		to    *int
	}{{f.Owner, false, &uid}, {f.Group, true, &gid}} {
		if x.name == "" {
			continue
		}
		n, err := a.account(ctx, x.name, x.group)
		if err != nil {
			if !a.dry {
				return o, nil, err
			}
			ownerGap = err.Error()
			continue
		}
		*x.to = n
	}
	var why []string
	switch {
	case !exists:
		why = append(why, "new file")
	default:
		if !bytes.Equal(cur, want) {
			why = append(why, "content")
		}
		if int64(hdr.Mode)&0o777 != mode {
			why = append(why, "mode")
		}
		if hdr.Uid != uid || hdr.Gid != gid {
			why = append(why, "owner")
		}
	}
	if ownerGap != "" {
		why = append(why, "owner not resolved yet")
	}
	if len(why) == 0 {
		o.status = api.StepUnchanged
		return o, nil, nil
	}
	o.detail = strings.Join(why, ", ")
	if a.dry {
		o.status = api.StepWouldChange
		return o, nil, nil
	}
	// the final mode and owner are set on the temporary file before it is renamed: no moment shows the content with other rights
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: path.Base(f.Path), Mode: mode, Uid: uid, Gid: gid, Size: int64(len(want)), ModTime: a.s.now()}); err != nil {
		return o, nil, err
	}
	if _, err := tw.Write(want); err != nil {
		return o, nil, err
	}
	if err := tw.Close(); err != nil {
		return o, nil, err
	}
	_, err = a.s.d.JailFiles.JailPut(ctx, JailPutReq{Jail: a.key, Root: a.root, Path: strings.TrimPrefix(path.Dir(f.Path), "/"), Mkdir: true, Preserve: true, MaxBytes: validate.RecipeFileMax + 1<<10}, &buf)
	if err != nil {
		return o, nil, err
	}
	o.status = api.StepChanged
	return o, nil, nil
}

// rcFile reads one of the rc.conf style files of the jail's /etc through the host's file operations (the minimal base
// set has no sysrc(8), and the file is the same either way).
func (a *applyRun) rcFile(ctx context.Context, name string) (string, string, error) {
	p := a.root + "/etc/" + name
	cur, err := a.s.d.Files.ReadFile(ctx, p)
	if err != nil && !notExist(err) {
		return "", p, fmt.Errorf("read %s: %w", p, err)
	}
	return string(cur), p, nil
}

// setRc sets the variables of vars that differ in the file and says which. rcNo/rcYes forms of a boolean are compared by
// the rc scripts' own rule (checkyesno), the others by text.
func (a *applyRun) setRc(ctx context.Context, name string, vars []render.RcVar, same func(v render.RcVar, cur string, ok bool) bool) ([]string, error) {
	cur, p, err := a.rcFile(ctx, name)
	if err != nil {
		return nil, err
	}
	var change []render.RcVar
	var names []string
	for _, v := range vars {
		c, ok := render.RcConfGet(cur, v.Name)
		if !same(v, c, ok) {
			change = append(change, v)
			names = append(names, v.Name+"="+v.Value)
		}
	}
	if len(change) == 0 || a.dry {
		return names, nil
	}
	next, err := render.RcConfSet(cur, change)
	if err != nil {
		return nil, err
	}
	return names, a.s.d.Files.WriteAtomic(ctx, p, []byte(next))
}

func (a *applyRun) sysrc(ctx context.Context, st api.RecipeStep) (stepOutcome, []byte, error) {
	file := st.In
	if file == "" {
		file = "rc.conf"
	}
	o := stepOutcome{kind: "sysrc", name: file}
	var vars []render.RcVar
	for _, k := range sortedKeys(st.Sysrc) {
		vars = append(vars, render.RcVar{Name: k, Value: st.Sysrc[k]})
	}
	names, err := a.setRc(ctx, file, vars, func(v render.RcVar, cur string, ok bool) bool { return ok && cur == v.Value })
	return a.rcOutcome(o, names), nil, err
}

// rcOutcome turns the list of variables a step changed (or would change) into its outcome.
func (a *applyRun) rcOutcome(o stepOutcome, names []string) stepOutcome {
	switch {
	case len(names) == 0:
		o.status = api.StepUnchanged
	case a.dry:
		o.status, o.detail = api.StepWouldChange, strings.Join(names, " ")
	default:
		o.status, o.detail = api.StepChanged, strings.Join(names, " ")
	}
	return o
}

// yes and no are the words checkyesno of rc.subr takes, in any case.
func rcBool(v string) (val, ok bool) {
	switch strings.ToLower(v) {
	case "yes", "true", "on", "1":
		return true, true
	case "no", "false", "off", "0":
		return false, true
	}
	return false, false
}

func (a *applyRun) service(ctx context.Context, sv api.RecipeService) (stepOutcome, []byte, error) {
	o := stepOutcome{kind: "service", name: sv.Name}
	var did []string
	if sv.Enabled != nil {
		v := render.RcVar{Name: svcVarRE.ReplaceAllString(sv.Name, "_") + "_enable", Value: "NO"}
		if *sv.Enabled {
			v.Value = "YES"
		}
		names, err := a.setRc(ctx, "rc.conf", []render.RcVar{v}, func(v render.RcVar, cur string, ok bool) bool {
			b, isBool := rcBool(cur)
			return ok && isBool && b == (v.Value == "YES")
		})
		if err != nil {
			return o, nil, err
		}
		if len(names) > 0 {
			did = append(did, map[bool]string{true: "enabled", false: "disabled"}[*sv.Enabled])
		}
	}
	if sv.Running != nil {
		// the one question put to the jail by a dry run too: a read-only query of the rc script
		_, err := a.inJail(ctx, time.Minute, "service", sv.Name, "status")
		running := err == nil
		if running != *sv.Running {
			verb, argv := "started", []string(nil)
			if *sv.Running {
				// through the fixed wrapper that keeps a daemon the rc script starts off the exec's pipes
				if argv, err = apps.ServiceStartArgv(sv.Name); err != nil {
					return o, nil, err
				}
			} else {
				verb, argv = "stopped", []string{"service", sv.Name, "stop"}
			}
			did = append(did, verb)
			if !a.dry {
				if out, err := a.inJail(ctx, 10*time.Minute, argv...); err != nil {
					o.detail = strings.Join(did[:len(did)-1], ", ")
					return o, out, err
				}
			}
		}
	}
	switch {
	case len(did) == 0:
		o.status = api.StepUnchanged
	case a.dry:
		o.status, o.detail = api.StepWouldChange, strings.Join(did, ", ")
	default:
		o.status, o.detail = api.StepChanged, strings.Join(did, ", ")
	}
	return o, nil, nil
}

func (a *applyRun) command(ctx context.Context, st api.RecipeStep) (stepOutcome, []byte, error) {
	o := stepOutcome{kind: "run", name: strings.Join(st.Run, " ")}
	guarded := st.Creates != "" || st.Unless != nil
	if st.Creates != "" {
		stat, err := a.s.d.JailFiles.JailStat(ctx, JailStatReq{Jail: a.key, Root: a.root, Path: strings.TrimPrefix(st.Creates, "/")})
		if err != nil {
			return o, nil, err
		}
		if stat.Type != api.FileTypeNone {
			o.status, o.detail = api.StepSkipped, "creates "+st.Creates+" exists"
			return o, nil, nil
		}
	}
	if a.dry {
		switch {
		case st.Unless != nil:
			o.status, o.detail = api.StepWouldCheck, "unless "+strings.Join(st.Unless, " ")
		case guarded:
			o.status = api.StepWouldChange
		default:
			o.status, o.detail = api.StepWouldExecute, "no guard"
		}
		return o, nil, nil
	}
	if st.Unless != nil {
		if _, err := a.inJail(ctx, 5*time.Minute, st.Unless...); err == nil {
			o.status, o.detail = api.StepSkipped, "unless "+strings.Join(st.Unless, " ")
			return o, nil, nil
		}
	}
	if out, err := a.inJail(ctx, 30*time.Minute, st.Run...); err != nil {
		return o, out, err
	}
	o.status = api.StepChanged
	if !guarded {
		o.status, o.detail = api.StepExecuted, "no guard"
	}
	return o, nil, nil
}

var _ = errors.New
