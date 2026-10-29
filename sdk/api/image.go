package api

import "time"

// Image is a named line of immutable base generations, built from pkgbase.
// Images are ScopeFleet objects; generations are replicated as datasets.
type Image struct {
	Metadata ObjectMeta  `json:"metadata"`
	Spec     ImageSpec   `json:"spec"`
	Status   ImageStatus `json:"status"`
}

// ImageSpec is the desired definition of an image.
type ImageSpec struct {
	OS      OS     `json:"os"`
	Release string `json:"release"` // "15.0-RELEASE" for FreeBSD, "3.22" for Alpine
	Arch    string `json:"arch"`    // "amd64", "arm64"
	// Source describes where the base comes from.
	Source ImageSource `json:"source"`
	// Aliases are movable names such as "stable" or "latest".
	Aliases []string `json:"aliases,omitempty"`
	// Retention keeps generations that no jail uses for a while.
	Retention ImageRetention `json:"retention,omitempty"`
}

// ImageSource describes how a generation is built.
type ImageSource struct {
	Type string `json:"type"` // "pkgbase" (FreeBSD), "minirootfs" (Alpine), "debootstrap"
	// Repo is the package repository or rootfs mirror.
	Repo string `json:"repo,omitempty"`
	// Sets lists pkgbase sets, for example "FreeBSD-set-minimal-jail".
	Sets []string `json:"sets,omitempty"`
	// Keys pins the signing keys. FreeBSD pkgbase uses the keys under
	// /usr/share/keys/pkgbase-<major>.
	Keys string `json:"keys,omitempty"`
}

// ImageRetention is how long unused generations are kept.
type ImageRetention struct {
	KeepLast int `json:"keep_last,omitempty"`
	KeepDays int `json:"keep_days,omitempty"`
}

// ImageStatus is the observed state of an image on one host.
type ImageStatus struct {
	Generations []ImageGeneration `json:"generations,omitempty"`
	// Aliases maps alias to generation number.
	Aliases map[string]int `json:"aliases,omitempty"`
}

// ImageGeneration is one immutable build of an image. Its dataset has an @ro
// snapshot that never changes. A generation is deleted only when nothing
// references it.
type ImageGeneration struct {
	Number int `json:"number"`
	// Name is "<image>#g<number>".
	Name string `json:"name"`
	// Parent is the generation this one was built from, if any.
	Parent int `json:"parent,omitempty"`
	// Manifest is a hash over the sorted installed package set and versions.
	Manifest  string    `json:"manifest"`
	Packages  int       `json:"packages,omitempty"`
	Dataset   string    `json:"dataset,omitempty"`
	CreatedAt time.Time `json:"created_at,omitzero"`
	// Refs lists jails that use this generation.
	Refs []string `json:"refs,omitempty"`
	// Storages lists the storage whose pool holds a copy. Clones need the
	// generation on the jail's own pool, so each pool keeps its own copy.
	Storages []string `json:"storages,omitempty"`
}

// ImageBuild is the body of POST /api/v1/images/{name}/generations.
type ImageBuild struct {
	// FromGeneration builds the next generation from an earlier one by
	// upgrading its packages; zero builds from the source.
	FromGeneration int  `json:"from_generation,omitempty"`
	DryRun         bool `json:"dry_run,omitempty"`
	// Storage names the storage whose pool is built on; empty means the
	// storage named "default". Other pools receive the generation when a jail there
	// needs it.
	Storage string `json:"storage,omitempty"`
}

// ImageUpgrade is the body of POST /api/v1/images/{name}/upgrade: build the
// next generation from the newest one, then move the thin jails that use the
// image to it, one at a time, stopping at the first that fails.
type ImageUpgrade struct {
	// NoJails builds the generation and moves no jail.
	NoJails bool `json:"no_jails,omitempty"`
	// Project restricts the jails moved to one project; empty means every
	// project the caller may administer.
	Project string `json:"project,omitempty"`
	// DryRun plans the build and lists the jails that would move; nothing changes.
	DryRun bool `json:"dry_run,omitempty"`
	// Storage names the storage whose pool is built on; see ImageBuild.Storage.
	Storage string `json:"storage,omitempty"`
}

// Jail outcomes of an image upgrade.
const (
	ImageUpgradeUpgraded     = "upgraded"
	ImageUpgradeSkipped      = "skipped"
	ImageUpgradeFailed       = "failed"
	ImageUpgradeNotAttempted = "not_attempted"
	// ImageUpgradePlanned is the outcome of a dry run: the jail would move.
	ImageUpgradePlanned = "planned"
)

// ImageUpgradeJail is what an image upgrade did with one jail.
type ImageUpgradeJail struct {
	Jail    string `json:"jail"`
	Project string `json:"project"`
	// Status is upgraded, skipped, failed, not_attempted or planned.
	Status string `json:"status"`
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
	// Reason says why the jail was skipped, failed or not attempted.
	Reason string `json:"reason,omitempty"`
	// Record is the id of the update history record the move wrote.
	Record string `json:"record,omitempty"`
}

// ImageUpgradeResult is the result of the image upgrade operation.
type ImageUpgradeResult struct {
	// From is the generation the build started from.
	From string `json:"from"`
	// Generation is the generation that was built (planned, for a dry run).
	// It is empty when the package set did not change.
	Generation *ImageGeneration `json:"generation,omitempty"`
	// Unchanged is true when the newest generation already holds the current
	// package set: no generation was kept and no jail was touched.
	Unchanged bool     `json:"unchanged,omitempty"`
	Plan      []string `json:"plan,omitempty"`
	DryRun    bool     `json:"dry_run,omitempty"`
	// Jails lists every jail that uses the image, with its outcome.
	Jails []ImageUpgradeJail `json:"jails"`
}

// ImageAliasSet is the body of PUT /api/v1/images/{name}/aliases/{alias}.
type ImageAliasSet struct {
	// Generation is the generation number the alias points at; zero means the newest.
	Generation int `json:"generation,omitempty"`
}
