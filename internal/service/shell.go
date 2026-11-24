package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"jailops.net/jailops/internal/render"
	"jailops.net/jailops/internal/validate"
	"jailops.net/jailops/sdk/api"
)

// The shell of a FreeBSD jail and its prompt (docs/shell.md).
//
// A new FreeBSD jail gets the defaults of jailopsd.yaml (jails.shell, jails.prompt) in its spec when its request
// and its profiles leave the keys empty, so `jailops config show` shows what it got. The shell key is the preferred
// shell of `jailops shell` and the console; JailOps never installs a shell: a jail whose shell is bash gets it when
// an administrator installs the package in the jail (`jailops exec NAME -- pkg install bash`), and until then
// `jailops shell` fails with a clear error. At every start JailOps writes the prompt the jail's key names:
// api.PromptFile, a marked block that sources it at the top of api.ProfilePath and api.BashrcPath (read by bash when
// it is installed) and at the end of api.ShrcPath (sh). A failure does not fail the start: it is a ShellSetup
// condition of the jail, and `jailops jail shell-setup` writes the files again.
//
// Existing jails are never changed on their own: their keys are empty, and empty means "leave the files alone".
// `jailops jail shell-setup NAME` gives such a jail the keys and prompt a new one gets. A changed prompt key is
// written at once into a running jail (under the jail's lock, by the update), or at the next start of a stopped one.
// Linux jails are left alone: the prompt key is refused for them.

// shellMark is the per-jail record of the prompt, in <StateDir>/shell/<key>.json. Older records may hold the fields
// pending and started of the bash installation JailOps no longer does; they are ignored.
type shellMark struct {
	// Applied is what the prompt file last said (promptState): the PS1 text, or "none".
	Applied string `json:"applied,omitempty"`
	// Error is the latest failure of writing the prompt (empty after a success), At its time.
	Error string    `json:"error,omitempty"`
	At    time.Time `json:"at,omitzero"`
}

type shellMarks struct {
	mu sync.Mutex
	m  map[string]shellMark
}

func (s *Service) shellPath(key string) string {
	return filepath.Join(s.d.StateDir, "shell", key+".json")
}

func (s *Service) shellMark(key string) shellMark {
	s.shellM.mu.Lock()
	defer s.shellM.mu.Unlock()
	if m, ok := s.shellM.m[key]; ok {
		return m
	}
	var m shellMark
	if s.d.StateDir != "" {
		if b, err := os.ReadFile(s.shellPath(key)); err == nil {
			_ = json.Unmarshal(b, &m)
		}
	}
	if s.shellM.m == nil {
		s.shellM.m = map[string]shellMark{}
	}
	s.shellM.m[key] = m
	return m
}

func (s *Service) setShellMark(key string, m shellMark) {
	s.shellM.mu.Lock()
	if s.shellM.m == nil {
		s.shellM.m = map[string]shellMark{}
	}
	s.shellM.m[key] = m
	s.shellM.mu.Unlock()
	if s.d.StateDir == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(s.shellPath(key)), 0o750)
	b, _ := json.Marshal(m)
	tmp := s.shellPath(key) + ".tmp"
	if os.WriteFile(tmp, b, 0o640) == nil {
		_ = os.Rename(tmp, s.shellPath(key))
	}
}

// forgetJailState drops what the daemon keeps in memory and in its state directory for a deleted jail: its shell mark
// and its update records.
func (s *Service) forgetJailState(key string) {
	s.forgetShell(key)
	s.upd.forget(key)
}

func (s *Service) forgetShell(key string) {
	s.shellM.mu.Lock()
	delete(s.shellM.m, key)
	s.shellM.mu.Unlock()
	if s.d.StateDir != "" {
		_ = os.Remove(s.shellPath(key))
	}
}

// shellCondition is the jail's ShellSetup condition: present while the latest writing of the prompt failed.
func (s *Service) shellCondition(key string) ([]api.Condition, *api.ShellSetupStatus) {
	m := s.shellMark(key)
	if m.Error == "" {
		return nil, nil
	}
	_, name := validate.SplitQualified(key)
	return []api.Condition{{Type: "ShellSetup", Status: api.ConditionFalse, Reason: "Failed", Since: m.At,
			Message: m.Error + "; try again with `jailops jail shell-setup " + name + "`"}},
		&api.ShellSetupStatus{State: api.ShellSetupFailed}
}

// promptContext is where the jail key is: the jail, its project and the short name of the host. The jail.conf that is
// rendered for every start carries it as the parameters env.jailops_jail, env.jailops_project and env.jailops_host, so it is the host the
// jail is started on that is named, whichever host that is.
func (s *Service) promptContext(key string) api.PromptContext {
	project, name := validate.SplitQualified(key)
	return api.PromptContext{Jail: name, Project: project, Host: api.ShortHost(s.d.Hostname)}
}

func (s *Service) promptColor() int {
	if c := s.d.JailPromptColor; c > 0 && c < 256 {
		return c
	}
	return api.DefaultPromptColor
}

// shellDefaults fills the shell and prompt keys of a new FreeBSD jail from jailopsd.yaml where neither the spec nor
// its profiles set them. The first start writes the prompt.
func (s *Service) shellDefaults(spec *api.JailSpec, profs []api.Profile) {
	if spec.OS != api.OSFreeBSD {
		return
	}
	spec.Shell = api.ShellPath(spec.Shell)
	eff, _ := api.ExpandedConfig(*spec, profs)
	shell := eff.Shell
	if shell == "" && s.d.JailShell == api.ShellBash {
		spec.Shell, shell = api.BashPath, api.BashPath
	}
	// the host's prompt goes into the spec of a jail with bash or sh; "none" on a jail that keeps sh has nothing to say
	if eff.Prompt == "" && s.d.JailPrompt != "" && (shell == api.BashPath || s.d.JailPrompt != api.PromptNone) {
		spec.Prompt = s.d.JailPrompt
	}
}

// promptState is what the prompt key means for the files: the PS1 text, "none", or "" (leave them alone).
func (s *Service) promptState(key string, eff api.JailSpec) string {
	switch eff.Prompt {
	case "":
		return ""
	case api.PromptNone:
		return api.PromptNone
	}
	return api.EffectivePS1(eff.Prompt, s.promptColor())
}

func notExist(err error) bool {
	return errors.Is(err, os.ErrNotExist) || api.IsCode(err, api.CodeNotFound)
}

// applyPrompt writes what the files of the jail key stand for into the jail root: the variables script (the same text
// in every jail), the prompt file (unless the prompt is none, which removes it and keeps the variables) and the block
// at the top of the two start-up files that sources them. ps1 is promptState. It returns what it did.
func (s *Service) applyPrompt(ctx context.Context, key, ps1 string) ([]string, error) {
	root := s.rootMount(key)
	f := s.d.Files
	var steps []string
	write := func(path, body string) error {
		if cur, err := f.ReadFile(ctx, root+path); err == nil && string(cur) == body {
			return nil
		}
		if err := f.WriteAtomic(ctx, root+path, []byte(body)); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
		steps = append(steps, "wrote "+path)
		return nil
	}
	if err := write(api.EnvFile, render.EnvScript()); err != nil {
		return steps, err
	}
	withPrompt := ps1 != api.PromptNone
	if withPrompt {
		body, err := render.PromptFile(ps1)
		if err != nil {
			return steps, err
		}
		if err := write(api.PromptFile, body); err != nil {
			return steps, err
		}
	} else if ok, _ := f.Exists(ctx, root+api.PromptFile); ok {
		if err := f.RemoveAll(ctx, root+api.PromptFile); err != nil {
			return steps, fmt.Errorf("remove %s: %w", api.PromptFile, err)
		}
		steps = append(steps, "removed "+api.PromptFile)
	}
	for _, p := range []string{api.ProfilePath, api.BashrcPath} {
		cur, err := f.ReadFile(ctx, root+p)
		if err != nil && !notExist(err) {
			return steps, fmt.Errorf("read %s: %w", p, err)
		}
		next, err := render.PromptHookSet(string(cur))
		if err != nil {
			// the text is the renderer's own (the file's path inside the jail, no host path): the reader needs it to fix the file
			return steps, api.Errorf(api.CodeInvalid, "%s: %v", p, err)
		}
		if next == string(cur) {
			continue
		}
		if err := f.WriteAtomic(ctx, root+p, []byte(next)); err != nil {
			return steps, fmt.Errorf("write %s: %w", p, err)
		}
		steps = append(steps, "put the jailops block at the top of "+p)
	}
	// FreeBSD's sh: root's .shrc sets PS1 after the files of profile.d, so the block that sources the prompt goes at its end
	// (a jail without a root .shrc has none to hook)
	if cur, err := f.ReadFile(ctx, root+api.ShrcPath); err == nil {
		next, err := render.ShrcHookSet(string(cur), withPrompt)
		if err != nil {
			return steps, api.Errorf(api.CodeInvalid, "%s: %v", api.ShrcPath, err)
		}
		if next != string(cur) {
			if err := f.WriteAtomic(ctx, root+api.ShrcPath, []byte(next)); err != nil {
				return steps, fmt.Errorf("write %s: %w", api.ShrcPath, err)
			}
			steps = append(steps, "put the jailops block at the end of "+api.ShrcPath)
		}
	} else if !notExist(err) {
		return steps, fmt.Errorf("read %s: %w", api.ShrcPath, err)
	}
	if len(steps) == 0 {
		steps = append(steps, "the shell files are in place: "+api.EnvFile+" and "+api.PromptFile+", sourced from "+api.ProfilePath+" and "+api.BashrcPath)
	}
	return steps, nil
}

// holdInfo says what holds a jail's package work and since when.
type holdInfo struct {
	What  string
	Since time.Time
}

// holdJailFor keeps exec, shell and console sessions out of a jail, and a second package operation away, while pkg works
// in it (an operator's session must not change the packages meanwhile): the operation is named what, for the message of
// whoever is refused. It refuses with `busy` when another package operation already holds the jail. The caller calls
// release when the package work is done.
func (s *Service) holdJailFor(key, what string) (release func(), err error) {
	s.execMu.Lock()
	if s.installing[key] {
		h := s.holds[key]
		s.execMu.Unlock()
		return nil, api.Errorf(api.CodeBusy, "%s is working on this jail", holdWords(h, s.now())).WithHint("wait for it to end; `jailops op list` shows it")
	}
	s.installing[key] = true
	s.holds[key] = holdInfo{What: what, Since: s.now()}
	s.execMu.Unlock()
	return func() {
		s.execMu.Lock()
		delete(s.installing, key)
		delete(s.holds, key)
		s.execMu.Unlock()
	}, nil
}

// holdWords names the operation that holds a jail and how long it has: "a package update (started 33 s ago)".
func holdWords(h holdInfo, now time.Time) string {
	what := h.What
	if what == "" {
		what = "a package operation"
	}
	if h.Since.IsZero() {
		return what
	}
	return fmt.Sprintf("%s (started %d s ago)", what, int(now.Sub(h.Since).Seconds()))
}

// promptDeadline is how long writing the prompt after a start may take.
var promptDeadline = 60 * time.Second

// shellAtStart runs after a jail started: it writes the prompt of a new jail, or a prompt that changed while it was
// stopped. It installs nothing and never fails the start; a failure is the jail's ShellSetup condition and an event.
func (s *Service) shellAtStart(ctx context.Context, key string) {
	ctx, cancel := context.WithTimeout(ctx, promptDeadline)
	defer cancel()
	j, err := s.jailSnapshot(key)
	if err != nil || j.Spec.OS != api.OSFreeBSD {
		return
	}
	eff := s.effective(j)
	if st := s.promptState(key, eff); st == "" || st == s.shellMark(key).Applied {
		return
	}
	if err := s.writePromptMark(ctx, key, eff); err != nil {
		s.log.Warn("prompt", "jail", key, "err", err)
		s.emitIn(ctx, j.Metadata.Project, "jail-shell-setup-failed", "/api/v1/jails/"+j.Metadata.Name)
	}
}

// shellInRoot says whether the program sh (a path inside the jail) exists in the jail's root.
func (s *Service) shellInRoot(ctx context.Context, key, sh string) bool {
	ok, _ := s.d.Files.Exists(ctx, path.Join(s.rootMount(key), sh))
	return ok
}

// writePromptMark applies the prompt of eff and records it.
func (s *Service) writePromptMark(ctx context.Context, key string, eff api.JailSpec) error {
	_, err := s.setUpPrompt(ctx, key, eff)
	return err
}

// setUpPrompt writes the prompt eff names, when it names one, and records the outcome in the jail's mark. It
// returns what it did.
func (s *Service) setUpPrompt(ctx context.Context, key string, eff api.JailSpec) ([]string, error) {
	st := s.promptState(key, eff)
	if st == "" {
		return nil, nil
	}
	steps, err := s.applyPrompt(ctx, key, st)
	m := s.shellMark(key)
	m.At, m.Error = s.now().UTC(), ""
	if err != nil {
		s.log.Warn("writing the prompt failed", "jail", key, "err", err)
		m.Error = "writing the prompt failed (the output is in the daemon log)"
	} else {
		m.Applied = st
	}
	s.setShellMark(key, m)
	return steps, err
}

// promptChanged writes a changed prompt into a running jail; a stopped one gets it at its next start. The caller
// holds the jail's lock. A failure is logged and kept as the ShellSetup condition: the change itself is stored.
func (s *Service) promptChanged(ctx context.Context, key string, old, nj api.Jail) {
	if nj.Spec.OS != api.OSFreeBSD {
		return
	}
	eff := s.effective(nj)
	st := s.promptState(key, eff)
	if st == "" || st == s.promptState(key, s.effective(old)) {
		return
	}
	if !s.jailRunning(ctx, key) {
		return
	}
	if err := s.writePromptMark(ctx, key, eff); err != nil {
		s.log.Warn("prompt", "jail", key, "err", err)
	}
}

// ShellSetupJail gives a FreeBSD jail the shell key and prompt a new jail gets (docs/shell.md): the keys the jail and
// its profiles leave empty are filled from jailopsd.yaml and stored, and the prompt is written. It installs nothing:
// a shell the jail does not have is named in the steps with the command that installs it. The jail must run.
func (s *Service) ShellSetupJail(ctx context.Context, name string, _ api.ShellSetupRequest) (api.Operation, error) {
	if err := s.requireHost("jails"); err != nil {
		return api.Operation{}, err
	}
	p, key, err := s.jailRef(ctx, name)
	if err != nil {
		return api.Operation{}, err
	}
	j, err := s.jailSnapshot(key)
	if err != nil {
		return api.Operation{}, err
	}
	if j.Spec.OS != api.OSFreeBSD {
		return api.Operation{}, invalid("jail %q is a %s jail: the shell setup is for FreeBSD jails", name, j.Spec.OS).
			WithHint("a Linux jail keeps the shell and prompt of its distribution")
	}
	if !s.jailRunning(ctx, key) {
		return api.Operation{}, api.Errorf(api.CodeBusy, "jail %q is not running; the prompt is written inside it", name).WithHint("start it first")
	}
	return s.Submit(ctx, OpSpec{Action: api.OpJailShellSetup, Description: "Apply the shell defaults and the prompt of jail " + name, Locks: []string{jailLock(key)}, Project: p.Meta.Name,
		Resources: map[string][]string{"jails": {"/api/v1/jails/" + name}},
		Run: func(ctx context.Context, h *OpHandle) (any, error) {
			cur, err := s.jailSnapshot(key)
			if err != nil {
				return nil, err
			}
			profs, _ := s.resolveProfiles(p.Meta.Name, cur.Spec.Profiles)
			spec := cur.Spec
			s.shellDefaults(&spec, profs)
			if spec.Shell != cur.Spec.Shell || spec.Prompt != cur.Spec.Prompt {
				in := cur
				in.Spec = spec
				if _, err := s.updateJail(ctx, p, key, name, in, AnyGeneration, true); err != nil {
					return nil, err
				}
				if cur, err = s.jailSnapshot(key); err != nil {
					return nil, err
				}
			}
			eff := s.effective(cur)
			if eff.Shell == "" {
				eff.Shell = DefaultShell
			}
			steps, err := s.setUpPrompt(ctx, key, eff)
			if err != nil {
				return nil, err
			}
			if !s.shellInRoot(ctx, key, eff.Shell) {
				steps = append(steps, shellMissing(name, eff.Shell))
			}
			s.emitIn(ctx, p.Meta.Name, "jail-shell-set-up", "/api/v1/jails/"+name)
			if len(steps) == 0 {
				steps = []string{"nothing to do"}
			}
			return api.ShellSetupResult{Jail: name, Shell: eff.Shell, Prompt: eff.Prompt, PS1: api.EffectivePS1(eff.Prompt, s.promptColor()), Steps: steps}, nil
		}})
}

// shellStepDescribe says what setUpPrompt does for a spec, for the steps of an app installation; empty when it does
// nothing.
func (s *Service) shellStepDescribe(key string, spec api.JailSpec) string {
	switch st := s.promptState(key, spec); {
	case spec.OS != api.OSFreeBSD || st == "":
		return ""
	case st == api.PromptNone:
		return "write " + api.EnvFile + " (where the jail is), no prompt"
	default:
		return "write " + api.EnvFile + " (where the jail is) and the prompt " + api.PromptFile + ", sourced from " + api.ProfilePath + ", " + api.BashrcPath + " and " + api.ShrcPath
	}
}

// isShellFile reports whether a line names one of the files the shell setup writes.
func isShellFile(l string) bool {
	switch strings.TrimSpace(l) {
	case api.PromptFile, api.EnvFile, api.ProfilePath, api.BashrcPath:
		return true
	}
	return false
}

// dropLines returns out without the lines drop matches.
func dropLines(out []byte, drop func(string) bool) []byte {
	var b strings.Builder
	for _, l := range strings.SplitAfter(string(out), "\n") {
		if l != "" && !drop(strings.TrimRight(l, "\n")) {
			b.WriteString(l)
		}
	}
	return []byte(b.String())
}

// pkgCache is the directory in which the host keeps the packages it downloaded for jail roots: the cache of the
// image builds, so that the second jail, and the next one after a first image build, need no download.
func (s *Service) pkgCache() string {
	if s.lcfg.MountBase == "" {
		return ""
	}
	return path.Join(s.lcfg.MountBase, "cache", "pkg")
}
