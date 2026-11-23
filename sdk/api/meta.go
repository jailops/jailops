// Package api defines the wire types of the JailOps management API.
//
// These types are the single source of truth for the REST API (/api/v1), the
// declarative YAML used by jailops-sync, the JSON written to the state
// dataset, and the Go SDK. The package depends on the standard library only.
//
// Every resource separates desired state (Spec) from observed state (Status).
// Spec is what a user, a Raft log or a fleet repository asks for. Status is
// what one host observed about the operating system, and it never enters a
// replicated log.
package api

import (
	"time"
)

// Version is the API version served under /api/v1.
const Version = "1.0"

// APIVersion is the apiVersion string used in declarative documents.
const APIVersion = "jailops/v1"

// Kind names a resource type in declarative documents and in the state store.
type Kind string

const (
	KindJail            Kind = "Jail"
	KindImage           Kind = "Image"
	KindNetwork         Kind = "Network"
	KindFirewallPolicy  Kind = "FirewallPolicy"
	KindAddressSet      Kind = "AddressSet"
	KindBackupPolicy    Kind = "BackupPolicy"
	KindBackupTarget    Kind = "BackupTarget"
	KindHostGroup       Kind = "HostGroup"
	KindRollout         Kind = "Rollout"
	KindJailTemplate    Kind = "JailTemplate"
	KindRemote          Kind = "Remote"
	KindUser            Kind = "User"
	KindToken           Kind = "Token"
	KindCertificate     Kind = "Certificate"
	KindHost            Kind = "Host"
	KindImageGeneration Kind = "ImageGeneration"
	KindJailSnapshot    Kind = "JailSnapshot"
	KindBackup          Kind = "Backup"
	KindOperation       Kind = "Operation"
	KindProject         Kind = "Project"
	KindStorage         Kind = "Storage"
	KindVolume          Kind = "Volume"
	KindWireGuard       Kind = "WireGuard"
	KindHostIdentity    Kind = "HostIdentity" // the singleton host identity object
	KindLogForward      Kind = "LogForward"   // the singleton list of log sinks
	KindHostFirewall    Kind = "HostFirewall" // the singleton host firewall object
	// the certificate of the console and the API
	KindHostCertificate Kind = "HostCertificate"
)

// Scope says how an object is managed.
//
// Every object is authoritative on the host that stores it: ScopeHost objects
// live on exactly one host (jails, volumes, projects, storage), and ScopeFleet
// objects (images, networks, policies, address sets, profiles, backup
// policies, targets, remotes, certificates, users, tokens, WireGuard tunnels)
// are objects that the hub and the fleet repository may manage with one
// definition. ScopeFleet does not mean shared state: each host keeps its own
// copy and the host's files are the truth. Shared authority, a replicated log
// from which the local files would be a projection, belongs to cluster mode,
// which is not built. Authorities lists the owner of every kind.
type Scope string

const (
	ScopeHost  Scope = "host"
	ScopeFleet Scope = "fleet"
)

// ObjectMeta is the identity and bookkeeping shared by all resources.
type ObjectMeta struct {
	// Name is unique per kind within its scope (per host for ScopeHost).
	Name string `json:"name" yaml:"name"`
	// UUID names one incarnation of an object: assigned at creation, never reused, and kept by a rename and by a
	// recovery or move that is meant to keep the logical identity (restore with replace, a future move). Copying a
	// jail and restoring a backup under another name (restore --as) make a new object with a new UUID. The same
	// name created again after a delete is a new object with a new UUID, and its Generation counts from 1 again, so a
	// generation alone never tells two incarnations apart: the ETag of an object carries both, so If-Match fails for
	// a replacement. Cross-host references use UUIDs; the same project name on two
	// hosts is two local objects until a shared project identity exists.
	UUID string `json:"uuid,omitempty" yaml:"uuid,omitempty"`
	// Project is the namespace of a project-level object (jails, volumes,
	// firewall policies, address sets, snapshots, backup policies). A project-level
	// object always carries its project; host-level objects have none. There is no
	// default project.
	Project string `json:"project,omitempty" yaml:"project,omitempty"`
	// Host is the UUID of the host that owns a ScopeHost object.
	Host string `json:"host,omitempty" yaml:"host,omitempty"`
	// Scope is fixed per kind and reported for clients.
	Scope Scope `json:"scope,omitempty" yaml:"scope,omitempty"`
	// Generation increases on every change to Spec. It doubles as the ETag
	// source: an update with a stale generation is rejected.
	Generation int64 `json:"generation,omitempty" yaml:"generation,omitempty"`
	// Labels select objects (backup policies, rollouts, host groups).
	Labels map[string]string `json:"labels,omitempty" yaml:"labels,omitempty"`
	// ManagedBy marks objects created by jailops-sync, for example
	// "sync:<fleet-id>". jailops-sync never changes or deletes objects it does
	// not own.
	ManagedBy string `json:"managed_by,omitempty" yaml:"managed_by,omitempty"`
	// CreatedAt and UpdatedAt are set by the server. The proposer of a
	// replicated command supplies them so that applying the command is
	// deterministic.
	CreatedAt time.Time `json:"created_at,omitzero" yaml:"created_at,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitzero" yaml:"updated_at,omitempty"`
}

// Condition reports one observed fact about an object as a typed
// True/False/Unknown status with a reason and a message. Conditions live in
// Status only.
type Condition struct {
	Type    string    `json:"type"`
	Status  string    `json:"status"` // "True", "False" or "Unknown"
	Reason  string    `json:"reason,omitempty"`
	Message string    `json:"message,omitempty"`
	Since   time.Time `json:"since,omitzero"`
}

// Problem reports whether the condition is a fault that someone should look at: any condition that is not True, except a
// jail that is stopped (Ready is False with the reason ConditionStopped, and nothing is wrong).
func (c Condition) Problem() bool {
	return c.Status != ConditionTrue && !(c.Type == "Ready" && c.Reason == ConditionStopped)
}

// ConditionStopped is the reason of Ready=False for a jail that is not running.
const ConditionStopped = "Stopped"

// Condition status values.
const (
	ConditionTrue    = "True"
	ConditionFalse   = "False"
	ConditionUnknown = "Unknown"
)

// ConditionFirewallApplied is the type of the condition that says whether the kernel has what the policies of a jail
// say: pf is enabled and evaluates the anchors of JailOps, the anchor of the jail holds the rendered rules, its tables
// hold the rendered members and the connections of removed rules are ended. A policy that is stored but not yet
// loaded is False ("NotLoaded"). The other reasons are "PFNotApplying" (pf disabled, the line anchor "jailops/*"
// missing from its main ruleset, or the ruleset unreadable) and "StatesPending" (pf refused to end the connections of a
// removed rule; the next pass tries again). When the loop could not ask pf whether it evaluates the anchors (pfctl gave
// no answer or no status) the condition is Unknown with the reason "Unverified": the rendered rules are loaded but
// nothing says they are applied. True has the reason "Applied" and names the rules by a short hash.
const ConditionFirewallApplied = "FirewallApplied"

// ConditionUnsupportedLayout is the condition of a jail whose root is a clone of an image with separate datasets for
// /etc, /usr/local and /var, a root layout of JailOps 0.9.2 and before that this release does not support. The jail
// does not start; a running one can be stopped, it can be deleted and read, and every other operation is refused.
// Status True, reason "EarlierRelease"; the message names the fix: create a new jail and move the data over, or
// convert it with `jailops jail convert NAME --layout thick` on that earlier release before the upgrade.
const ConditionUnsupportedLayout = "UnsupportedLayout"

// The states of status.shell_setup.
const (
	// ShellSetupInstalling is kept for the frozen contract and is never reported: JailOps no longer installs a shell
	// (it did, in the background, before 0.9.3).
	ShellSetupInstalling = "installing"
	// ShellSetupFailed: the latest writing of the prompt failed; the ShellSetup condition says why.
	ShellSetupFailed = "failed"
)

// ShellInstalling is kept for the frozen contract and is never reported: it was the reason of the ShellSetup condition
// while bash was being installed, which JailOps no longer does.
const ShellInstalling = "Installing"

// Document is the declarative envelope used in YAML files and in the state
// dataset. Spec is decoded according to Kind.
type Document struct {
	APIVersion string     `json:"apiVersion" yaml:"apiVersion"`
	Kind       Kind       `json:"kind" yaml:"kind"`
	Metadata   ObjectMeta `json:"metadata" yaml:"metadata"`
	Spec       any        `json:"spec" yaml:"spec"`
}
