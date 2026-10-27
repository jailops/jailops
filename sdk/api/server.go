package api

import (
	"encoding/json"
	"time"
)

// ServerInfo is returned by GET /api/v1.
type ServerInfo struct {
	APIVersion string `json:"api_version"`
	// APIExtensions lists capability names. Clients use them instead of
	// version numbers, which is what lets a hub manage hosts of different
	// versions.
	APIExtensions []string `json:"api_extensions"`
	Version       string   `json:"version"`
	HostUUID      string   `json:"host_uuid"`
	Hostname      string   `json:"hostname"`
	Role          Role     `json:"role"`
	Hub           bool     `json:"hub,omitempty"`
	Clustered     bool     `json:"clustered,omitempty"`
	// Auth describes the caller as the server sees it.
	Auth        string      `json:"auth"` // "trusted", "untrusted"
	Environment Environment `json:"environment"`
	// SecretsRecipient is the age public key sealed environment values are encrypted to ("sealed_secrets").
	SecretsRecipient string `json:"secrets_recipient,omitempty"`
}

// ForUntrusted is what a caller that proved nothing is told: the API version, the role (so a console can tell a host from a
// backup node) and that it is untrusted. No software version, host name, host id or environment: those are for callers
// with an identity, and a console asks again after it signed in.
func (i ServerInfo) ForUntrusted() ServerInfo {
	return ServerInfo{APIVersion: i.APIVersion, APIExtensions: []string{}, Role: i.Role, Auth: i.Auth}
}

// Environment is the observed host environment.
type Environment struct {
	OS            string `json:"os"`
	KernelVersion string `json:"kernel_version"`
	Arch          string `json:"arch"`
	ZFSVersion    string `json:"zfs_version,omitempty"`
	Pool          string `json:"pool,omitempty"`
	PF            bool   `json:"pf,omitempty"`
	Linuxulator   bool   `json:"linuxulator,omitempty"`
}

// ResponseType is the kind of an API response envelope.
type ResponseType string

const (
	ResponseSync  ResponseType = "sync"
	ResponseAsync ResponseType = "async"
	ResponseError ResponseType = "error"
)

// Response is the envelope of every API reply.
type Response struct {
	Type       ResponseType `json:"type"`
	Status     string       `json:"status"`
	StatusCode int          `json:"status_code"`
	// Operation is the URL of the operation for async responses.
	Operation string          `json:"operation,omitempty"`
	Error     *Error          `json:"error,omitempty"`
	Metadata  json.RawMessage `json:"metadata,omitempty"`
}

// OperationStatus is the state of an asynchronous operation.
type OperationStatus string

const (
	OpPending   OperationStatus = "pending"
	OpRunning   OperationStatus = "running"
	OpSuccess   OperationStatus = "success"
	OpFailure   OperationStatus = "failure"
	OpCancelled OperationStatus = "cancelled"
)

// Final reports whether the status is terminal.
func (s OperationStatus) Final() bool {
	return s == OpSuccess || s == OpFailure || s == OpCancelled
}

// Operation is a long-running task. Operations are persisted, so a daemon
// restart can resume or roll back.
type Operation struct {
	ID          string          `json:"id"`
	Description string          `json:"description"`
	Status      OperationStatus `json:"status"`
	// Resources lists affected objects by kind, for example
	// {"jails": ["/api/v1/jails/web"]}.
	Resources map[string][]string `json:"resources,omitempty"`
	Metadata  json.RawMessage     `json:"metadata,omitempty"`
	// Project is the project the operation belongs to; empty for host-level
	// operations (networks, images, storage).
	Project   string    `json:"project,omitempty"`
	Error     *Error    `json:"error,omitempty"`
	MayCancel bool      `json:"may_cancel"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// Progress is a 0..100 estimate when the operation can compute one.
	Progress int `json:"progress,omitempty"`
}

// EventType names an event stream class.
type EventType string

const (
	EventLifecycle EventType = "lifecycle"
	EventOperation EventType = "operation"
	EventLogging   EventType = "logging"
)

// Event is delivered on /api/v1/events.
type Event struct {
	Type      EventType       `json:"type"`
	Timestamp time.Time       `json:"timestamp"`
	Host      string          `json:"host,omitempty"`
	Metadata  json.RawMessage `json:"metadata"`
}

// LifecycleEvent is the metadata of an EventLifecycle event.
type LifecycleEvent struct {
	Action string `json:"action"` // jail-started, jail-created, network-updated, ...
	Source string `json:"source"` // /api/v1/jails/web
	// Requestor identifies who caused the event.
	Requestor string `json:"requestor,omitempty"`
	// Project is the project of the object; empty for host-level objects,
	// whose events only global identities see.
	Project string `json:"project,omitempty"`
}

// Extensions is the capability list of this API revision. New names are
// appended; names are never removed or renamed.
var Extensions = []string{
	"operations",
	"etag",
	"events",
	"jails",
	"jail_snapshots",
	"jail_rollback_scopes",
	"images_pkgbase",
	"image_generations",
	"networks_vnet",
	"network_ipam_range",
	"firewall_anchors",
	"backup_policies",
	"backup_pull",
	"backup_push",
	"restore_preflight",
	"audit_log",
	"doctor",
	"resources",
	"jail_copy",
	"projects",
	"project_quotas",
	"storage",
	"trust_tokens",
	"sessions",
	"fleet_projects",
	"config_keys",
	"jail_config",
	"profiles",
	"jail_devices",
	"jail_exec",
	"jail_console",
	"backup_key",
	"backup_run",
	"backup_verify",
	"updates",
	"jail_shell",
	"provisioning",
	"sealed_secrets",
	"image_aliases",
}
