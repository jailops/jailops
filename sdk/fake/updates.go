package fake

import (
	"net/http"
	"sort"
	"strings"

	"jailops.net/jailops/sdk/api"
)

// The fake's managed updates and recipes. A test seeds the package
// updates a jail has (SeedPackageUpdates) and can make the next health check
// fail (SeedUpdateFailure); image generations come from SeedGeneration.

// SeedPackageUpdates sets the package updates a check of the jail finds.
func (s *Server) SeedPackageUpdates(project, jail string, pkgs []api.PackageUpdate) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.updPkgs == nil {
		s.updPkgs = map[string][]api.PackageUpdate{}
	}
	s.updPkgs[project+"/"+jail] = pkgs
}

// SeedUpdateFailure makes applying updates to the jail fail its health check.
func (s *Server) SeedUpdateFailure(project, jail string, fail bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.updFail == nil {
		s.updFail = map[string]bool{}
	}
	s.updFail[project+"/"+jail] = fail
}

func (s *Server) updKey(j *api.Jail) string { return j.Metadata.Project + "/" + j.Metadata.Name }

// errLinuxUpdates is the daemon's refusal of updates for a Linux jail.
func errLinuxUpdates() *api.Error {
	return api.Errorf(api.CodeUnsupported, "Linux jails look after their own packages: JailOps does not update them").
		WithHint("run apk or apt in the jail, for instance from its cron")
}

// updateTargets resolves the jails of a request in the request's project. Linux jails are left out, and one named
// alone is refused, as the daemon does.
func (s *Server) updateTargets(rc *reqInfo, names []string, all bool) ([]*api.Jail, *api.Error) {
	var out []*api.Jail
	if all {
		var keys []string
		for k, j := range s.jails.items {
			if j.Metadata.Project == rc.project && j.Spec.OS != api.OSLinux {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			out = append(out, s.jails.items[k])
		}
		return out, nil
	}
	if len(names) == 0 {
		return nil, api.Errorf(api.CodeInvalid, "name the jails, or ask for all of them")
	}
	for _, n := range names {
		j := s.jails.items[s.jails.keyIn(rc.project, n)]
		if j == nil {
			return nil, notFound("jail", n)
		}
		if j.Spec.OS == api.OSLinux {
			if len(names) == 1 {
				return nil, errLinuxUpdates()
			}
			continue
		}
		out = append(out, j)
	}
	return out, nil
}

func (s *Server) checkReport(j *api.Jail) api.UpdateReport {
	rep := api.UpdateReport{Jail: j.Metadata.Name, Project: j.Metadata.Project, Mode: j.Spec.Update.Mode, CheckedAt: s.now()}
	if j.Status.Power == api.PowerRunning {
		rep.Packages = append(rep.Packages, s.updPkgs[s.updKey(j)]...)
	} else {
		rep.Error = "the jail is stopped; its packages cannot be checked"
	}
	// the fake keeps no base packages: the base of a jail is never behind
	rep.Available = len(rep.Packages) > 0
	return rep
}

func (s *Server) handleUpdatesList(w http.ResponseWriter, r *http.Request) {
	rc := s.info(r)
	s.mu.Lock()
	var keys []string
	for k, rep := range s.updReports {
		if rc.id.Global || containsString(rc.id.Projects, rep.Project) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	out := []api.UpdateReport{}
	for _, k := range keys {
		out = append(out, s.updReports[k])
	}
	s.mu.Unlock()
	writeSync(w, 200, out)
}

func (s *Server) handleUpdatesCheck(w http.ResponseWriter, r *http.Request) {
	req, e := decodeOptional[api.UpdateCheckRequest](r)
	if e != nil {
		writeErr(w, e)
		return
	}
	rc := s.info(r)
	s.mu.Lock()
	rp := func() reply {
		targets, e := s.updateTargets(rc, req.Jails, req.All)
		if e != nil {
			return reply{err: e}
		}
		op := s.startOp(rc.project, "updates.check", "Check for updates in project "+rc.project, nil, func() (any, *api.Error) {
			out := []api.UpdateReport{}
			if s.updReports == nil {
				s.updReports = map[string]api.UpdateReport{}
			}
			for _, j := range targets {
				rep := s.checkReport(j)
				s.updReports[s.updKey(j)] = rep
				out = append(out, rep)
			}
			return out, nil
		})
		return reply{op: &op}
	}()
	s.mu.Unlock()
	s.respond(w, rp)
}

func (s *Server) handleUpdatesApply(w http.ResponseWriter, r *http.Request) {
	req, e := decodeOptional[api.UpdateApplyRequest](r)
	if e != nil {
		writeErr(w, e)
		return
	}
	rc := s.info(r)
	s.mu.Lock()
	rp := func() reply {
		targets, e := s.updateTargets(rc, req.Jails, req.All)
		if e != nil {
			return reply{err: e}
		}
		op := s.startOp(rc.project, "updates.apply", "Apply updates in project "+rc.project, nil, func() (any, *api.Error) {
			out := []api.UpdateRecord{}
			for _, j := range targets {
				out = append(out, s.applyTo(j, req)...)
			}
			return out, nil
		})
		return reply{op: &op}
	}()
	s.mu.Unlock()
	s.respond(w, rp)
}

// applyTo is the pipeline of one jail: OS first, then packages; a failed
// health check records the failure (an OS update is swapped back).
func (s *Server) applyTo(j *api.Jail, req api.UpdateApplyRequest) []api.UpdateRecord {
	var recs []api.UpdateRecord
	add := func(r api.UpdateRecord) {
		r.ID = "upd-" + randHex(4)
		r.Jail, r.Project, r.StartedAt, r.FinishedAt = j.Metadata.Name, j.Metadata.Project, s.now(), s.now()
		if s.updHistory == nil {
			s.updHistory = map[string][]api.UpdateRecord{}
		}
		s.updHistory[s.updKey(j)] = append(s.updHistory[s.updKey(j)], r)
		recs = append(recs, r)
		if r.Result == api.UpdateSuccess {
			s.emitIn(j.Metadata.Project, "update.applied", "/api/v1/jails/"+j.Metadata.Name+"/updates")
		} else {
			s.emitIn(j.Metadata.Project, "update.failed", "/api/v1/jails/"+j.Metadata.Name+"/updates")
		}
	}
	both := !req.Packages && !req.OS
	failing := s.updFail[s.updKey(j)]
	if (req.Packages || both) && len(s.updPkgs[s.updKey(j)]) > 0 && j.Status.Power == api.PowerRunning {
		rec := api.UpdateRecord{Kind: api.UpdateKindPackages, Packages: s.updPkgs[s.updKey(j)], Snapshot: "update-" + randHex(3)}
		if failing {
			rec.Result, rec.Error = api.UpdateFailure, "health check failed"
		} else {
			rec.Result = api.UpdateSuccess
			delete(s.updPkgs, s.updKey(j))
		}
		add(rec)
	}
	return recs
}

func (s *Server) handleUpdateHistory(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	_, j := s.jails.find(r, r.PathValue("name"))
	var out []api.UpdateRecord
	if j != nil {
		h := s.updHistory[s.updKey(j)]
		for i := len(h) - 1; i >= 0; i-- {
			out = append(out, h[i])
		}
	}
	s.mu.Unlock()
	if j == nil {
		writeErr(w, notFound("jail", r.PathValue("name")))
		return
	}
	if j.Spec.OS == api.OSLinux {
		writeErr(w, errLinuxUpdates())
		return
	}
	if out == nil {
		out = []api.UpdateRecord{}
	}
	writeSync(w, 200, out)
}

// handleRecipeValidate answers a recipe with steps as valid: the fake has the number of steps of the real validator and
// nothing else of it.
func (s *Server) handleRecipeValidate(w http.ResponseWriter, r *http.Request) {
	req, e := decodeOptional[api.RecipeValidateRequest](r)
	if e != nil {
		writeErr(w, e)
		return
	}
	if n := len(req.Recipe.Steps); n == 0 || n > 200 {
		writeErr(w, api.Errorf(api.CodeInvalid, "steps: a recipe has between 1 and 200 steps"))
		return
	}
	writeSync(w, 200, api.RecipeValidateResult{Valid: true, Steps: len(req.Recipe.Steps)})
}

// handleApply reports every step of a recipe as done (an unguarded run as executed); a dry run reports would change.
func (s *Server) handleApply(w http.ResponseWriter, r *http.Request) {
	req, e := decodeOptional[api.ApplyRequest](r)
	if e != nil {
		writeErr(w, e)
		return
	}
	name := r.PathValue("name")
	rc := s.info(r)
	jk := s.jails.key(r, name)
	s.mu.Lock()
	rp := func() reply {
		if s.jails.items[jk] == nil {
			return reply{err: notFound("jail", name)}
		}
		op := s.startOp(rc.project, "jail.apply", "Apply a recipe to jail "+name, map[string][]string{"jails": {"/api/v1/jails/" + name}}, func() (any, *api.Error) {
			j := s.jails.items[jk]
			if j == nil {
				return nil, notFound("jail", name)
			}
			if j.Status.Power != api.PowerRunning {
				return nil, api.Errorf(api.CodeBusy, "jail %q is not running; a recipe runs inside it", name).WithHint("start it first")
			}
			res := api.ApplyResult{Jail: name, DryRun: req.DryRun, Steps: []api.ApplyStep{}}
			done := s.apply.done[jk]
			if done == nil {
				done = map[string]bool{}
				if s.apply.done == nil {
					s.apply.done = map[string]map[string]bool{}
				}
				s.apply.done[jk] = done
			}
			s.apply.calls = append(s.apply.calls, ApplyCall{Project: rc.project, Jail: name, Req: req})
			for i, st := range req.Recipe.Steps {
				kind, label := "run", strings.Join(st.Run, " ")
				switch {
				case st.Package != nil:
					kind, label = "package", strings.Join(st.Package, " ")
				case st.File != nil:
					kind, label = "file", st.File.Path
				case st.Dir != nil:
					kind, label = "dir", st.Dir.Path
				case st.Sysrc != nil:
					kind, label = "sysrc", "rc.conf"
				case st.Service != nil:
					kind, label = "service", st.Service.Name
				}
				key := stepKey(st)
				unguarded := kind == "run" && st.Creates == "" && st.Unless == nil
				if f, ok := s.apply.fault[jk]; ok && !req.DryRun && f.step == i {
					delete(s.apply.fault, jk)
					return nil, api.Errorf(api.CodeStepFailed, "step %d of %d failed (%s %s): %s", i+1, len(req.Recipe.Steps), kind, label, f.msg)
				}
				status := api.StepChanged
				switch {
				case unguarded && req.DryRun:
					status = api.StepWouldExecute
				case unguarded:
					status = api.StepExecuted
				case kind == "run" && st.Unless != nil && req.DryRun:
					status = api.StepWouldCheck
				case done[key]:
					status = api.StepUnchanged
				case req.DryRun:
					status = api.StepWouldChange
				}
				if !req.DryRun && !unguarded {
					done[key] = true
				}
				res.Steps = append(res.Steps, api.ApplyStep{Kind: kind, Name: label, Status: status})
			}
			if !req.DryRun {
				s.emitIn(rc.project, "jail-applied", "/api/v1/jails/"+name)
			}
			return res, nil
		})
		return reply{op: &op}
	}()
	s.mu.Unlock()
	s.respond(w, rp)
}

// handleShellSetup answers like the daemon of a host whose jails.shell is bash: a running FreeBSD jail gets bash as its
// preferred shell and the default prompt where its keys are empty, nothing is installed, and the steps say what a
// host would do (the fake jail has no bash).
func (s *Server) handleShellSetup(w http.ResponseWriter, r *http.Request) {
	if _, e := decodeOptional[api.ShellSetupRequest](r); e != nil {
		writeErr(w, e)
		return
	}
	name := r.PathValue("name")
	rc := s.info(r)
	jk := s.jails.key(r, name)
	s.mu.Lock()
	rp := func() reply {
		j := s.jails.items[jk]
		if j == nil {
			return reply{err: notFound("jail", name)}
		}
		if j.Spec.OS == api.OSLinux {
			return reply{err: api.Errorf(api.CodeInvalid, "jail %q is a linux jail: the shell setup is for FreeBSD jails", name)}
		}
		if j.Status.Power != api.PowerRunning {
			return reply{err: api.Errorf(api.CodeBusy, "jail %q is not running; the prompt is written inside it", name).WithHint("start it first")}
		}
		op := s.startOp(rc.project, "jails.shell-setup", "Apply the shell defaults and the prompt of jail "+name, map[string][]string{"jails": {"/api/v1/jails/" + name}}, func() (any, *api.Error) {
			j := s.jails.items[jk]
			if j == nil {
				return nil, notFound("jail", name)
			}
			if j.Spec.Shell == "" {
				j.Spec.Shell = api.BashPath
			}
			if j.Spec.Prompt == "" && j.Spec.Shell == api.BashPath {
				j.Spec.Prompt = api.PromptDefault
			}
			steps := []string{}
			ps1 := api.EffectivePS1(j.Spec.Prompt, api.DefaultPromptColor)
			if ps1 != "" {
				steps = append(steps, "wrote "+api.PromptFile)
			}
			if j.Spec.Shell == api.BashPath {
				steps = append(steps, "the shell "+api.BashPath+" is not installed in jail "+name+"; install it with: jailops pkg install "+name+" -- bash, or use sh: jailops shell "+name+" --shell "+api.ShPath)
			}
			s.emitIn(rc.project, "jail-shell-set-up", "/api/v1/jails/"+name)
			return api.ShellSetupResult{Jail: name, Shell: j.Spec.Shell, Prompt: j.Spec.Prompt, PS1: ps1, Steps: steps}, nil
		})
		return reply{op: &op}
	}()
	s.mu.Unlock()
	s.respond(w, rp)
}

func containsString(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}
