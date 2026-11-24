package api

import (
	"fmt"
	"strings"
	"time"
)

// Recipe is what `jailops jail apply` runs inside a running FreeBSD jail: a list of steps, in the order of the list.
// Every step checks the state it is about and changes only what differs, so a second apply of the same recipe changes
// nothing. There is no agent in the jail and no convergence: the operator runs it, when they want.
type Recipe struct {
	Steps []RecipeStep `json:"steps"`
}

// RecipeStep is one step: exactly one of Package, File, Sysrc, Service and Run is set. Creates and Unless are the
// guards of a Run step; In names the file of a Sysrc step.
type RecipeStep struct {
	// Package lists packages that must be installed; only the missing ones are installed.
	Package []string          `json:"package,omitempty"`
	File    *RecipeFile       `json:"file,omitempty"`
	Sysrc   map[string]string `json:"sysrc,omitempty"`
	// In is the file of a Sysrc step, below /etc of the jail: rc.conf (the default) or periodic.conf.
	In      string         `json:"in,omitempty"`
	Service *RecipeService `json:"service,omitempty"`
	// Run is a command as argv, never a shell. It runs with the login PATH of the jail.
	Run []string `json:"run,omitempty"`
	// Creates skips a Run step when this path exists in the jail.
	Creates string `json:"creates,omitempty"`
	// Unless skips a Run step when this command (argv) exits 0 in the jail.
	Unless []string `json:"unless,omitempty"`
}

// RecipeFile puts a file into the jail. Content is the text of the file; the client fills it from Source (a file
// beside the recipe) before it sends the request, and the daemon never reads a path of the host.
type RecipeFile struct {
	Path    string  `json:"path"`
	Source  string  `json:"source,omitempty"`
	Content *string `json:"content,omitempty"`
	// Mode is octal, "0644" or "644"; empty is 0644.
	Mode  string `json:"mode,omitempty"`
	Owner string `json:"owner,omitempty"`
	Group string `json:"group,omitempty"`
}

// RecipeService sets the rc.conf variable NAME_enable (Enabled) and starts (Running true) or stops (Running false) the
// service when it is not already in that state. A service is never restarted.
type RecipeService struct {
	Name    string `json:"name"`
	Enabled *bool  `json:"enabled,omitempty"`
	Running *bool  `json:"running,omitempty"`
}

// ApplyRequest is the body of POST /api/v1/jails/{name}/apply.
type ApplyRequest struct {
	Recipe Recipe `json:"recipe"`
	// DryRun reads the state and reports what would change; it runs no command in the jail and writes nothing.
	DryRun bool `json:"dry_run,omitempty"`
}

// The Status values of an ApplyStep. A dry run reports WouldChange, WouldCheck and WouldExecute in place of the
// changes: nothing was done.
const (
	StepChanged      = "changed"
	StepUnchanged    = "unchanged"
	StepSkipped      = "skipped"
	StepExecuted     = "executed" // a run without guard: JailOps cannot know whether it changed anything
	StepFailed       = "failed"
	StepWouldChange  = "would change"
	StepWouldCheck   = "would check"
	StepWouldExecute = "would execute"
)

// ApplyStep is the report of one step.
type ApplyStep struct {
	// Kind is package, file, sysrc, service or run.
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Status string `json:"status"`
	// Detail says what was done or why the step was skipped.
	Detail string `json:"detail,omitempty"`
}

// ApplyResult is the result of the apply operation.
type ApplyResult struct {
	Jail   string      `json:"jail"`
	DryRun bool        `json:"dry_run,omitempty"`
	Steps  []ApplyStep `json:"steps"`
}

// Lines renders the steps for a terminal, one per line, and a summary line.
func (r ApplyResult) Lines() []string {
	var out []string
	count := map[string]int{}
	unguarded := 0
	for _, st := range r.Steps {
		count[st.Status]++
		if st.Status == StepExecuted || st.Status == StepWouldExecute {
			unguarded++
		}
		l := fmt.Sprintf("%-8s %-38s %s", st.Kind, st.Name, st.Status)
		if st.Detail != "" {
			l += "  (" + st.Detail + ")"
		}
		out = append(out, l)
	}
	var sum []string
	for _, k := range []string{StepChanged, StepWouldChange, StepUnchanged, StepSkipped, StepExecuted, StepWouldExecute, StepWouldCheck, StepFailed} {
		if count[k] > 0 {
			sum = append(sum, fmt.Sprintf("%d %s", count[k], k))
		}
	}
	line := fmt.Sprintf("%d steps: %s", len(r.Steps), strings.Join(sum, ", "))
	if unguarded > 0 {
		line += "; a run without creates or unless is executed every time"
	}
	return append(out, line)
}

// PackageUpdate is one package an update would change.
type PackageUpdate struct {
	Name string `json:"name"`
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
}

// OSUpdate is an image generation newer than the jail's root.
type OSUpdate struct {
	From string `json:"from"` // "freebsd-15#g3"
	To   string `json:"to"`
}

// UpdateReport is what an update check found for one jail. It lists updates;
// it changes nothing.
type UpdateReport struct {
	Jail     string          `json:"jail"`
	Project  string          `json:"project"`
	Mode     UpdateMode      `json:"mode,omitempty"`
	Packages []PackageUpdate `json:"packages,omitempty"`
	OS       *OSUpdate       `json:"os,omitempty"`
	// Available is true when something can be applied.
	Available bool      `json:"available"`
	CheckedAt time.Time `json:"checked_at"`
	// Error says why the check could not complete for this jail (for
	// example a stopped jail cannot be asked about its packages).
	Error string `json:"error,omitempty"`
}

// UpdateKind is what an update changed.
type UpdateKind string

// UpdateResult is how an update ended.
type UpdateResult string

// Update kinds and results.
const (
	UpdateKindPackages UpdateKind = "packages"
	UpdateKindOS       UpdateKind = "os"

	UpdateSuccess         UpdateResult = "success"
	UpdatePreflightFailed UpdateResult = "preflight_failed" // refused before anything changed
	UpdateRolledBack      UpdateResult = "rolled_back"      // failed and the jail is back as it was
	UpdateRecoveryNeeded  UpdateResult = "recovery_needed"  // failed and the jail needs a manual recovery
	UpdateFailure         UpdateResult = "failure"
)

// UpdateRecord is one entry of a jail's update history.
type UpdateRecord struct {
	ID         string       `json:"id"`
	Jail       string       `json:"jail"`
	Project    string       `json:"project"`
	Kind       UpdateKind   `json:"kind"`
	Result     UpdateResult `json:"result"`
	StartedAt  time.Time    `json:"started_at"`
	FinishedAt time.Time    `json:"finished_at"`
	// Snapshot is the atomic snapshot taken before the update. It is kept:
	// rolling a package update back is a data rollback (scope all), not a
	// root rollback.
	Snapshot string `json:"snapshot,omitempty"`
	From     string `json:"from,omitempty"` // generation, for an OS update
	To       string `json:"to,omitempty"`
	// Packages lists the packages the update changed.
	Packages []PackageUpdate `json:"packages,omitempty"`
	// RootRolledBack is true when a failed OS update put the previous root back. Application data was not touched.
	RootRolledBack bool `json:"root_rolled_back,omitempty"`
	// Scheduled is set for updates started by the schedule.
	Scheduled bool   `json:"scheduled,omitempty"`
	Error     string `json:"error,omitempty"`
	Note      string `json:"note,omitempty"`
}

// UpdateCheckRequest is the body of POST /api/v1/updates/check. Jails names the
// jails of the request's project; All checks every jail of the project.
type UpdateCheckRequest struct {
	Jails []string `json:"jails,omitempty"`
	All   bool     `json:"all,omitempty"`
}

// UpdateApplyRequest is the body of POST /api/v1/updates/apply.
type UpdateApplyRequest struct {
	Jails []string `json:"jails,omitempty"`
	// All applies to every jail of the project that has something to apply.
	All bool `json:"all,omitempty"`
	// Packages and OS restrict what is applied; both false applies what each
	// jail's policy allows (the policy's packages and os flags, or both when
	// the policy is manual).
	Packages bool `json:"packages,omitempty"`
	OS       bool `json:"os,omitempty"`
}
