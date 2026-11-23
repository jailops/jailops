package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"jailops.net/jailops/internal/supervise"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"jailops.net/jailops/internal/apps"
	"jailops.net/jailops/internal/render"
	"jailops.net/jailops/internal/validate"
	"jailops.net/jailops/sdk/api"
)

// The shell of a FreeBSD jail and its bash prompt (docs/shell.md).
//
// A new FreeBSD jail gets the defaults of jailopsd.yaml (jails.shell, jails.prompt) in its spec when its request
// and its profiles leave the keys empty, so `jailops config show` shows what it got. After its first start JailOps
// installs bash into the running jail when bash is its shell (pkg -j, inside the jail, in the background) and writes the
// prompt: api.PromptFile, and a marked block that sources it at the top of api.ProfilePath and api.BashrcPath.
// A failure does
// not fail the start: it is a ShellSetup condition of the jail, and `jailops jail shell-setup` tries again.
//
// Existing jails are never changed on their own: their keys are empty, and empty means "leave the files alone".
// `jailops jail shell-setup NAME` gives such a jail what a new one gets. A changed prompt key is written at once
// into a running jail (under the jail's lock, by the update), or at the next start of a stopped one.
// Linux jails are left alone: the prompt key is refused for them and nothing is installed.

// shellMark is the per-jail record of the setup, in <StateDir>/shell/<key>.json.
type shellMark struct {
	// Pending: the jail was created with a shell or prompt to set up at its first start.
	Pending bool `json:"pending,omitempty"`
	// Started: the jail has started at least once. A start never waits for a package server: the jail starts at
	// once and bash is fetched into the running jail in the background (pkg -j, inside the jail's confinement).
	Started bool `json:"started,omitempty"`
	// Applied is what the prompt file last said (promptState): the PS1 text, or "none".
	Applied string `json:"applied,omitempty"`
	// Error is the latest failure of the setup (empty after a success), At its time.
	Error string    `json:"error,omitempty"`
	At    time.Time `json:"at,omitzero"`
}

type shellMarks struct {
	mu sync.Mutex
	m  map[string]shellMark
	// bg is the set of jails whose bash is being installed in the background right now.
	bg map[string]bool
	// prog is where the setup of such a jail is: when it began and the step it is in now.
	prog map[string]shellProg
}

// shellProg is the progress of a setup of the shell: `jailops shell` shows it while it waits.
type shellProg struct {
	Started time.Time
	Step    string
	// Cancel ends the setup (SIGTERM to pkg, SIGKILL after shellTermGrace); Cancelled says it was asked to.
	Cancel    context.CancelFunc
	Cancelled bool
}

// shellStep records the step the setup of the jail's shell is in (the setup of a jail that is not in the
// background is not tracked).
func (s *Service) shellStep(key, step string) {
	s.shellM.mu.Lock()
	defer s.shellM.mu.Unlock()
	if p, ok := s.shellM.prog[key]; ok {
		p.Step = step
		s.shellM.prog[key] = p
	}
}

// shellStepOf is the step the background setup of the jail's shell is in, "" when there is none.
func (s *Service) shellStepOf(key string) string {
	s.shellM.mu.Lock()
	defer s.shellM.mu.Unlock()
	return s.shellM.prog[key].Step
}

// The steps of the setup of a shell, in the words of `jailops shell` while it waits.
const (
	stepFetchBash   = "fetching and installing bash with pkg (the package catalogue comes first)"
	stepLibraries   = "updating the library cache of the jail"
	stepPromptFiles = "writing the prompt"
)

// stopJail stops a jail the way every caller does, after the setup of its shell in the background has had a moment (a
// few seconds) or has been cancelled gently (settleShellSetup): pkg inside the jail is not cut off in the middle of its
// work (a jail whose pkg was killed by the stop stayed "dying" and kept its root busy), and a setup that hangs on a dead
// network does not make every stop wait for it.
func (s *Service) stopJail(ctx context.Context, key string) error {
	s.settleShellSetup(ctx, key)
	return s.life.StopJail(ctx, key)
}

// shellInBackground says whether the shell of the jail is being set up in the background now.
func (s *Service) shellInBackground(key string) bool {
	s.shellM.mu.Lock()
	defer s.shellM.mu.Unlock()
	return s.shellM.bg[key]
}

// waitShellSetup waits, at most d, for a background setup of the shell of the jail to end.
func (s *Service) waitShellSetup(ctx context.Context, key string, d time.Duration) {
	deadline := time.NewTimer(d)
	defer deadline.Stop()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for s.shellInBackground(key) {
		select {
		case <-ctx.Done():
			return
		case <-deadline.C:
			return
		case <-tick.C:
		}
	}
}

// shellSettleWait is how long a stop, a restart, an update or an app installation waits for the setup of the jail's
// shell, and shellTermGrace how long pkg has to end after SIGTERM before it is killed.
var (
	shellSettleWait = 3 * time.Second
	shellTermGrace  = 3 * time.Second
)

// settleShellSetup makes the background setup of the shell of the jail end before the caller goes on: it waits for it
// a few seconds, then cancels it (SIGTERM to pkg's process group, SIGKILL after shellTermGrace) and waits for that.
// The cancelled setup stays due: the next start tries again.
func (s *Service) settleShellSetup(ctx context.Context, key string) {
	if !s.shellInBackground(key) {
		return
	}
	s.waitShellSetup(ctx, key, shellSettleWait)
	if !s.shellInBackground(key) {
		return
	}
	s.shellM.mu.Lock()
	p, ok := s.shellM.prog[key]
	if ok {
		p.Cancelled = true
		s.shellM.prog[key] = p
	}
	s.shellM.mu.Unlock()
	if ok && p.Cancel != nil {
		p.Cancel()
	}
	s.waitShellSetup(ctx, key, shellTermGrace+3*time.Second)
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

func (s *Service) forgetShell(key string) {
	s.shellM.mu.Lock()
	delete(s.shellM.m, key)
	s.shellM.mu.Unlock()
	if s.d.StateDir != "" {
		_ = os.Remove(s.shellPath(key))
	}
}

// shellCondition is the jail's ShellSetup condition: present while bash is being installed in the background and
// while the latest setup failed.
func (s *Service) shellCondition(key string) ([]api.Condition, *api.ShellSetupStatus) {
	m := s.shellMark(key)
	if s.shellInBackground(key) {
		s.shellM.mu.Lock()
		p := s.shellM.prog[key]
		s.shellM.mu.Unlock()
		since := p.Started
		if since.IsZero() {
			since = s.now()
		}
		step := p.Step
		if step == "" {
			step = stepFetchBash
		}
		return []api.Condition{{Type: "ShellSetup", Status: api.ConditionUnknown, Reason: api.ShellInstalling, Since: since.UTC(),
				Message: "bash is being installed in the background: " + step + "; `jailops shell` waits for it"}},
			&api.ShellSetupStatus{State: api.ShellSetupInstalling, Step: step}
	}
	if m.Error == "" {
		return nil, nil
	}
	_, name := validate.SplitQualified(key)
	return []api.Condition{{Type: "ShellSetup", Status: api.ConditionFalse, Reason: "Failed", Since: m.At,
			Message: m.Error + "; try again with `jailops jail shell-setup " + name + "`, or choose FreeBSD's own shell with `jailops config set " + name + " shell sh`"}},
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
// its profiles set them, and reports whether there is anything to set up at the first start.
func (s *Service) shellDefaults(spec *api.JailSpec, profs []api.Profile) bool {
	if spec.OS != api.OSFreeBSD {
		return false
	}
	spec.Shell = api.ShellPath(spec.Shell)
	eff, _ := api.ExpandedConfig(*spec, profs)
	shell := eff.Shell
	if shell == "" && s.d.JailShell == api.ShellBash {
		spec.Shell, shell = api.BashPath, api.BashPath
	}
	prompt := eff.Prompt
	// the host's prompt goes into the spec of a jail with bash or sh; "none" on a jail that keeps sh has nothing to say
	if prompt == "" && s.d.JailPrompt != "" && (shell == api.BashPath || s.d.JailPrompt != api.PromptNone) {
		spec.Prompt, prompt = s.d.JailPrompt, s.d.JailPrompt
	}
	return shell == api.BashPath || (prompt != "" && prompt != api.PromptNone)
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
		next, err := render.PromptHookSet(string(cur), withPrompt)
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

// holdInfo says what holds a jail's package work and since when. Shell: it is only the setup of the shell (bash and the
// prompt), which a session that does not need bash may enter.
type holdInfo struct {
	What  string
	Since time.Time
	Shell bool
}

// holdJailFor keeps exec, shell and console sessions out of a jail, and a second package operation away, while pkg works
// in it (an operator's session must not change the packages meanwhile): the operation is named what, for the message of
// whoever is refused. It refuses with `busy` when another package operation already holds the jail. A holder that is
// not the setup of the shell first gives a background setup of the shell a few seconds and cancels it (settleShellSetup).
// shell is true for the setup of the shell itself, which does not keep a session that needs no bash out of the jail.
// The caller calls release when the package work is done.
func (s *Service) holdJailFor(key, what string, shell bool) (release func(), err error) {
	if !shell {
		s.settleShellSetup(context.Background(), key)
	}
	s.execMu.Lock()
	if s.installing[key] {
		h := s.holds[key]
		s.execMu.Unlock()
		return nil, api.Errorf(api.CodeBusy, "%s is working on this jail", holdWords(h, s.now())).WithHint("wait for it to end; `jailops op list` shows it")
	}
	s.installing[key] = true
	s.holds[key] = holdInfo{What: what, Since: s.now(), Shell: shell}
	s.execMu.Unlock()
	return func() {
		s.execMu.Lock()
		delete(s.installing, key)
		delete(s.holds, key)
		s.execMu.Unlock()
	}, nil
}

// holdWords names the operation that holds a jail and how long it has: "the setup of the shell (33 s)".
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

// installBash installs the bash package into a running FreeBSD jail with pkg -j (pkg enters the jail, so the host's
// root never writes into a root that runs), unless it is there. Exec sessions are kept out while pkg works, and the
// jail's library cache is refreshed afterwards; an app installation (inApp) does both itself.
func (s *Service) installBash(ctx context.Context, key string, inApp bool) ([]string, error) {
	guard := !inApp
	root := s.rootMount(key)
	if ok, _ := s.d.Files.Exists(ctx, root+api.BashPath); ok {
		return []string{api.BashPath + " is installed"}, nil
	}
	if s.d.Run == nil {
		return nil, api.Errorf(api.CodeUnsupported, "this daemon cannot run pkg on the host")
	}
	if guard {
		release, err := s.holdJailFor(key, "the setup of the shell", true)
		if err != nil {
			return nil, err
		}
		defer release()
	}
	// inside the running jail: over the jail's own network, which its firewall may close, so it does not wait long
	argv := apps.WithFetchLimits(apps.PkgInstallInJailArgv(key, []string{api.BashPackage}))
	s.shellStep(key, stepFetchBash)
	if err := s.runHostGrace(ctx, 5*time.Minute, argv, shellTermGrace); err != nil {
		return nil, err
	}
	steps := []string{strings.Join(argv, " ")}
	if inApp {
		return steps, nil
	}
	// the library cache of the running jail must learn about the libraries bash brought into /usr/local/lib.
	s.shellStep(key, stepLibraries)
	if _, err := s.runInJail(ctx, key, 5*time.Minute, "/sbin/ldconfig", "-m", "/usr/local/lib"); err != nil {
		return steps, err
	}
	return append(steps, "in the jail: /sbin/ldconfig -m /usr/local/lib"), nil
}

// setUpShell installs bash when the effective shell is bash and writes the prompt the spec names. It records the
// outcome in the jail's mark. inApp: an app installation runs it (installBash).
func (s *Service) setUpShell(ctx context.Context, key string, eff api.JailSpec, inApp bool) ([]string, error) {
	var steps []string
	var err error
	bashFailed := false
	if eff.Shell == api.BashPath {
		steps, err = s.installBash(ctx, key, inApp)
		bashFailed = err != nil
	}
	m := s.shellMark(key)
	// bash that could not be fetched stays due: the next start of the jail tries again from the host, whose network
	// and cache do not depend on the firewall of the jail
	m.Pending = bashFailed
	if err == nil {
		if ps1 := s.promptState(key, eff); ps1 != "" {
			var more []string
			s.shellStep(key, stepPromptFiles)
			more, err = s.applyPrompt(ctx, key, ps1)
			steps = append(steps, more...)
			if err == nil {
				m.Applied = s.promptState(key, eff)
			}
		}
	}
	m.At = s.now().UTC()
	m.Error = ""
	if err != nil {
		_, jname := validate.SplitQualified(key)
		m.Error = shellFailure(s.shellStepOf(key), jname, ctx, err)
	}
	s.setShellMark(key, m)
	return steps, err
}

// shellFailure is the text of a failed setup of the shell: the step it was in and the reason, which for a setup
// that ran out of time is the time and not "context deadline exceeded", and for pkg output that says the network
// did not answer, what to look at.
func shellFailure(step, jail string, ctx context.Context, err error) string {
	what := "setting up the shell failed"
	if step != "" {
		what += " (" + step + ")"
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		what += ": it did not finish in " + shellSetupDeadline.String()
		if unreachable(err) {
			return what + "; " + unreachableHint(jail)
		}
		return what + " (the package server or the jail's network may be slow or closed)"
	}
	// the output of pkg and of the helper can hold host paths and command lines: the jail's status, which every reader of the
	// jail sees, names the cause class and the commands; the output is in the daemon log ("shell setup" or "operation failed")
	if unreachable(err) {
		return what + ": " + unreachableHint(jail)
	}
	return what + ": the package manager or the jail did not complete it (the output is in the daemon log)"
}

// unreachable says whether the pkg output tells that the package server did not answer (a timeout, no route, a
// resolver that did not answer, a refused connection).
func unreachable(err error) bool {
	m := strings.ToLower(err.Error())
	for _, w := range []string{"timed out", "timeout", "no route to host", "network is unreachable", "transient resolver failure",
		"temporary failure in name resolution", "connection refused", "operation timed out"} {
		if strings.Contains(m, w) {
			return true
		}
	}
	return false
}

// unreachableHint names the cause and the commands that tell which address family does not work.
func unreachableHint(jail string) string {
	return "the jail could not reach the package server (pkg.freebsd.org) over IPv6 or IPv4; look with " +
		"`jailops exec " + jail + " -- host pkg.freebsd.org`, `jailops exec " + jail + " -- ping -6 -c 1 pkg.freebsd.org` and `ping -4`"
}

// shellSetupDeadline is how long the setup after a start may take.
var shellSetupDeadline = 60 * time.Second

// shellAtStart runs after a jail started: the setup of a new jail, or a prompt that changed while it was stopped.
// It never fails the start; a failure is the jail's ShellSetup condition and an event.
func (s *Service) shellAtStart(ctx context.Context, key string) {
	// A start never waits on the shell: what takes longer than this (a package server the jail cannot reach) is a
	// failed setup with its event, and the jail keeps the shell it has.
	ctx, cancel := context.WithTimeout(ctx, shellSetupDeadline)
	defer cancel()
	j, err := s.jailSnapshot(key)
	if err != nil || j.Spec.OS != api.OSFreeBSD {
		return
	}
	eff := s.effective(j)
	m := s.shellMark(key)
	if !m.Started {
		m.Started = true
		s.setShellMark(key, m)
	}
	switch {
	case m.Pending && eff.Shell == api.BashPath && !s.bashInRoot(ctx, key):
		// the jail is up with the shell it has, and bash is fetched from inside the running jail in the background, so
		// the start never waits for it: one attempt per start (`jailops jail shell-setup` asks for one at once)
		s.setUpShellInBackground(key, j.Metadata.Project, j.Metadata.Name)
		return
	case m.Pending:
		_, err = s.setUpShell(ctx, key, eff, false)
	case s.promptState(key, eff) != "" && s.promptState(key, eff) != m.Applied:
		err = s.writePromptMark(ctx, key, eff)
	default:
		return
	}
	if err != nil {
		s.log.Warn("shell setup", "jail", key, "err", err)
		s.emitIn(ctx, j.Metadata.Project, "jail-shell-setup-failed", "/api/v1/jails/"+j.Metadata.Name)
	}
}

// bashInRoot says whether the jail's root has bash.
func (s *Service) bashInRoot(ctx context.Context, key string) bool {
	ok, _ := s.d.Files.Exists(ctx, s.rootMount(key)+api.BashPath)
	return ok
}

// setUpShellInBackground runs the setup of the shell of a running jail without holding the operation that started
// it. A failure is the jail's ShellSetup condition and an event, as when the setup runs inside the start.
func (s *Service) setUpShellInBackground(key, project, name string) {
	s.shellM.mu.Lock()
	if s.shellM.bg[key] {
		s.shellM.mu.Unlock()
		return
	}
	if s.shellM.bg == nil {
		s.shellM.bg = map[string]bool{}
	}
	s.shellM.bg[key] = true // set here, not in the goroutine: the start that returns next reports it
	if s.shellM.prog == nil {
		s.shellM.prog = map[string]shellProg{}
	}
	base := s.baseCtx
	if base == nil {
		base = context.Background()
	}
	// The context is made here too, so that a stop that comes at once can cancel a setup whose goroutine has not run.
	ctx, cancel := context.WithTimeout(base, shellSetupDeadline)
	s.shellM.prog[key] = shellProg{Started: s.now(), Step: stepFetchBash, Cancel: cancel}
	if !s.track() {
		// the service is closing: no setup starts; the jail stays due and the next start tries again
		delete(s.shellM.bg, key)
		delete(s.shellM.prog, key)
		s.shellM.mu.Unlock()
		cancel()
		return
	}
	s.shellM.mu.Unlock()
	go func() {
		defer s.wg.Done()
		defer func() {
			s.shellM.mu.Lock()
			delete(s.shellM.bg, key)
			delete(s.shellM.prog, key)
			s.shellM.mu.Unlock()
		}()
		defer func() { supervise.Report(s.log, "shell-setup", recover()) }()
		defer cancel()
		j, err := s.jailSnapshot(key)
		if err != nil {
			return
		}
		if _, err := s.setUpShell(ctx, key, s.effective(j), false); err != nil {
			s.shellM.mu.Lock()
			cancelled := s.shellM.prog[key].Cancelled
			s.shellM.mu.Unlock()
			if cancelled {
				// The stop, restart, update or app installation that asked for the jail ended the setup: it stays due (the
				// next start tries again) and is no failure.
				m := s.shellMark(key)
				m.Pending, m.Error = true, ""
				s.setShellMark(key, m)
				s.log.Info("the setup of the shell was cancelled by the stop", "jail", key)
				s.emitIn(base, project, "jail-shell-setup-cancelled", "/api/v1/jails/"+name)
				return
			}
			s.log.Warn("shell setup", "jail", key, "err", err)
			s.emitIn(ctx, project, "jail-shell-setup-failed", "/api/v1/jails/"+name)
		}
	}()
}

// writePromptMark applies the prompt of eff and records it.
func (s *Service) writePromptMark(ctx context.Context, key string, eff api.JailSpec) error {
	_, err := s.applyPrompt(ctx, key, s.promptState(key, eff))
	m := s.shellMark(key)
	m.At, m.Error = s.now().UTC(), ""
	if err != nil {
		s.log.Warn("writing the prompt failed", "jail", key, "err", err)
		m.Error = "writing the prompt failed (the output is in the daemon log)"
	} else {
		m.Applied = s.promptState(key, eff)
	}
	s.setShellMark(key, m)
	return err
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

// ShellSetupJail gives a FreeBSD jail the shell and prompt a new jail gets (docs/shell.md): the keys the jail and
// its profiles leave empty are filled from jailopsd.yaml and stored, bash is installed into the jail's /usr/local
// from the host when the shell is bash and it is missing, and the prompt is written. The jail must run.
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
		return api.Operation{}, api.Errorf(api.CodeBusy, "jail %q is not running; the shell is set up inside it", name).WithHint("start it first")
	}
	return s.Submit(ctx, OpSpec{Action: api.OpJailShellSetup, Description: "Set up the shell of jail " + name, Locks: []string{jailLock(key)}, Project: p.Meta.Name,
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
			steps, err := s.setUpShell(ctx, key, eff, false)
			if err != nil {
				return nil, err
			}
			s.emitIn(ctx, p.Meta.Name, "jail-shell-set-up", "/api/v1/jails/"+name)
			if len(steps) == 0 {
				steps = []string{"nothing to do"}
			}
			return api.ShellSetupResult{Jail: name, Shell: eff.Shell, Prompt: eff.Prompt, PS1: api.EffectivePS1(eff.Prompt, s.promptColor()), Steps: steps}, nil
		}})
}

// shellStepDescribe says what setUpShell does for a spec, for the steps of an app installation; empty when it does
// nothing.
func (s *Service) shellStepDescribe(key string, spec api.JailSpec) string {
	var parts []string
	if spec.OS == api.OSFreeBSD && spec.Shell == api.BashPath {
		parts = append(parts, "install bash unless it is there ("+strings.Join(apps.PkgInstallInJailArgv(key, []string{api.BashPackage}), " ")+")")
	}
	switch st := s.promptState(key, spec); {
	case spec.OS != api.OSFreeBSD || st == "":
	case st == api.PromptNone:
		parts = append(parts, "write "+api.EnvFile+" (where the jail is), no prompt")
	default:
		parts = append(parts, "write "+api.EnvFile+" (where the jail is) and the bash prompt "+api.PromptFile+", sourced from "+api.ProfilePath+" and "+api.BashrcPath)
	}
	return strings.Join(parts, "; ")
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
