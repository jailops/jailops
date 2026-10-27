// Package wire holds the types and constants the backup node and its clients
// exchange. It has no dependencies, so a host can use the client without
// linking the node.
package wire

import "time"

// Base is the path prefix of the node API.
const Base = "/api/v1/repo"

// MaxMissingBatch is the most chunk ids one missing request accepts.
const MaxMissingBatch = 2000

// SessionInfo is what a host learns when it opens a session.
type SessionInfo struct {
	ID         string    `json:"id"`
	Group      string    `json:"group"`
	Snapshot   string    `json:"snapshot"`
	Lease      int       `json:"lease_seconds"`
	LeaseMs    int64     `json:"lease_ms"`
	LeaseUntil time.Time `json:"lease_expires_at"`
	// Previous is the previous snapshot the node pinned, "" when none was given or it is gone.
	Previous string `json:"previous,omitempty"`
}

// BeginRequest is the body of POST /api/v1/repo/sessions.
type BeginRequest struct {
	Group    string `json:"group"`
	Snapshot string `json:"snapshot"`
	Previous string `json:"previous,omitempty"`
	// KeyDomain is the non-secret domain id of the host's master key. A repository that recorded
	// a domain refuses a host with another one.
	KeyDomain string `json:"key_domain,omitempty"`
}

// MissingRequest is the body of POST .../missing.
type MissingRequest struct {
	IDs []string `json:"ids"`
}

// MissingResponse lists the ids the repository lacks.
type MissingResponse struct {
	Missing []string `json:"missing"`
}

// PutChunkResult says what a chunk upload did.
type PutChunkResult struct {
	// Created is true when this upload wrote the object.
	Created bool `json:"created"`
}

// IndexResponse answers an index upload.
type IndexResponse struct {
	Digest string `json:"digest"`
	Chunks int    `json:"chunks"`
}

// ManifestResponse answers a manifest upload.
type ManifestResponse struct {
	Digest string `json:"digest"`
}

// CommitRequest carries the digests the host computed of what it uploaded.
type CommitRequest struct {
	IndexDigest    string `json:"index_digest"`
	ManifestDigest string `json:"manifest_digest"`
}

// HeartbeatResponse carries the renewed lease.
type HeartbeatResponse struct {
	LeaseUntil time.Time `json:"lease_expires_at"`
}

// RepoInfo describes the repository to a host.
type RepoInfo struct {
	ID       string `json:"id"`
	Format   int    `json:"format"`
	Mode     string `json:"mode"`
	Backend  string `json:"backend"`
	ServerID string `json:"server_id"`
	// KeyDomain is the key domain the repository was initialised with, "" if none was recorded.
	KeyDomain string `json:"key_domain,omitempty"`
	// MaxBatch, MaxBlob and Lease are the limits a client should honour.
	MaxBatch int `json:"max_missing_batch"`
	MaxBlob  int `json:"max_blob_bytes"`
	Lease    int `json:"lease_seconds"`
}

// RestoreTest is the report of a restore test (deep, extract or full) run on a host that holds the key.
type RestoreTest struct {
	Group    string   `json:"group"`
	Snapshot string   `json:"snapshot"`
	Method   string   `json:"method"` // deep, extract, full
	OK       bool     `json:"ok"`
	Problems []string `json:"problems,omitempty"`
	Chunks   int      `json:"chunks,omitempty"`
}

// Group is the backups of one jail.
type Group struct {
	GroupID      string     `json:"group_id"`
	HostID       string     `json:"host_id,omitempty"`
	HostName     string     `json:"host_name,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	LastActivity *time.Time `json:"last_activity,omitempty"`
}

// Snapshot is a committed snapshot as the node indexes it.
type Snapshot struct {
	GroupID        string    `json:"group_id"`
	SnapshotID     string    `json:"snapshot_id"`
	CommittedAt    time.Time `json:"committed_at"`
	ChunkCount     int       `json:"chunk_count"`
	IndexBytes     int       `json:"index_bytes"`
	IndexDigest    string    `json:"index_digest"`
	ManifestDigest string    `json:"manifest_digest"`
	Source         string    `json:"source"`
	Protected      bool      `json:"protected"`
	// VerifyState is "", "ok" or "failed". A failed snapshot is never a base for an incremental run.
	VerifyState    string     `json:"verify_state"`
	VerifiedAt     *time.Time `json:"verified_at,omitempty"`
	VerifyProblems []string   `json:"verify_problems,omitempty"`
	// PendingDeletion is set while a deletion request waits for its delay.
	PendingDeletionRequestedAt *time.Time `json:"pending_deletion_requested_at,omitempty"`
	PendingDeletionAfter       *time.Time `json:"pending_deletion_after,omitempty"`
	PendingDeletionBy          string     `json:"pending_deletion_by,omitempty"`
}

// EscrowList is the answer of GET /api/v1/repo/escrow: the key domains that have an escrow blob.
type EscrowList struct {
	Domains []string `json:"domains"`
}
