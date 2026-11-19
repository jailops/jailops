package api

import "time"

// Provision is the declaration of a jail's provisioning (design 8.5). JailOps
// knows how to do each step on FreeBSD (pkg, files with owner and mode,
// sysrc, service) and on Alpine (apk and the init of that jail), so the user
// does not write those commands. Steps run in this order, once per jail;
// changing the declaration later does nothing until the user asks for
// `provision --again`.
type Provision struct {
	Packages []string        `json:"packages,omitempty"`
	Files    []ProvisionFile `json:"files,omitempty"`
	Services ProvisionSvc    `json:"services,omitempty"`
	// Commands run once, as argv, never through a shell.
	Commands [][]string `json:"commands,omitempty"`
}

// ProvisionFile copies a file of the project into the jail. Source is a path
// below the project's files directory, never an arbitrary host path.
type ProvisionFile struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Owner       string `json:"owner,omitempty"`
	Group       string `json:"group,omitempty"`
	// Mode is octal, "0644" or "644".
	Mode string `json:"mode,omitempty"`
}

// ProvisionSvc lists the services to enable and to start.
type ProvisionSvc struct {
	Enable []string `json:"enable,omitempty"`
	Start  []string `json:"start,omitempty"`
}

// ProvisionRequest is the body of POST /api/v1/jails/{name}/provision.
type ProvisionRequest struct {
	// Again runs provisioning although it has run before.
	Again bool `json:"again,omitempty"`
}

// ProvisionResult is the result of the provision operation.
type ProvisionResult struct {
	Jail          string    `json:"jail"`
	ProvisionedAt time.Time `json:"provisioned_at"`
	// Steps names what ran, in order.
	Steps []string `json:"steps"`
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

// Update kinds and results.
const (
	UpdateKindPackages = "packages"
	UpdateKindOS       = "os"

	UpdateOK         = "ok"
	UpdateFailed     = "failed"
	UpdateRolledBack = "rolled_back"
)

// UpdateRecord is one entry of a jail's update history.
type UpdateRecord struct {
	ID       string    `json:"id"`
	Jail     string    `json:"jail"`
	Project  string    `json:"project"`
	Kind     string    `json:"kind"` // packages or os
	Result   string    `json:"result"`
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished"`
	// Snapshot is the atomic snapshot taken before the update. It is kept:
	// rolling a package update back is a data rollback (scope all), not a
	// root rollback.
	Snapshot string `json:"snapshot,omitempty"`
	From     string `json:"from,omitempty"` // generation, for an OS update
	To       string `json:"to,omitempty"`
	// Packages lists the packages the update changed.
	Packages []PackageUpdate `json:"packages,omitempty"`
	// RootRolledBack is true when a failed OS update was swapped back to the
	// previous generation. Application data was not touched.
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
