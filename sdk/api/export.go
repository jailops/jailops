package api

import "time"

// ExportFormat is the version of the jail export file (docs/export-import.md).
const ExportFormat = 1

// The kinds of an ExportPart.
const (
	PartRoot   = "root"   // the root of the jail
	PartVolume = "volume" // a volume of the project, with --with-volumes
)

// ExportPart is one ZFS stream of an export file.
type ExportPart struct {
	// Dataset names the dataset relative to the jail (root); for a volume its own name.
	Dataset string `json:"dataset"`
	Kind    string `json:"kind"`
	// Snapshot is the snapshot the stream ends at; From, when set, makes it incremental from that snapshot.
	Snapshot string `json:"snapshot"`
	From     string `json:"from,omitempty"`
	// Raw marks a stream sent as stored (zfs send -w): an encrypted volume stays encrypted, and the file holds no key.
	Raw bool `json:"raw,omitempty"`
	// Compression is "zstd" when the stream is compressed in the file.
	Compression string `json:"compression,omitempty"`
	// Props are the ZFS properties the target sets on the dataset when it receives the first part of it.
	Props map[string]string `json:"props,omitempty"`
}

// ExportVolume is a volume carried in the file (--with-volumes).
type ExportVolume struct {
	Name   string            `json:"name"`
	Spec   VolumeSpec        `json:"spec"`
	Labels map[string]string `json:"labels,omitempty"`
	// Shared is true when other jails of the project attach the volume too: the file holds a copy, not the volume.
	Shared bool `json:"shared,omitempty"`
}

// ExportManifest is the first record of an export file. The jail, the objects it refers to and the datasets that
// follow are all named here; nothing the target needs to decide is only in the streams.
type ExportManifest struct {
	Format  int       `json:"format"`
	Created time.Time `json:"created"`
	// Jail is the name, project and uuid of the exported jail, and the labels and comment it had.
	Name    string            `json:"name"`
	Project string            `json:"project"`
	UUID    string            `json:"uuid"`
	Labels  map[string]string `json:"labels,omitempty"`
	OS      OS                `json:"os,omitempty"`
	// Bundle is the closure of the jail: its spec, frozen copies of the networks, firewall policies and address sets it
	// refers to, and the names of the sealed secrets it needs (never their values).
	Bundle  RestoreBundle  `json:"bundle"`
	Parts   []ExportPart   `json:"parts"`
	Volumes []ExportVolume `json:"volumes,omitempty"`
	// Warnings are what the export left out or did by value.
	Warnings []string `json:"warnings,omitempty"`
}

// JailExportRequest are the options of GET /api/v1/jails/{name}/export.
type JailExportRequest struct {
	// Snapshot exports that snapshot of the jail (name as in `jail snapshot list`) instead of a fresh one.
	Snapshot string
	// WithVolumes carries the volumes of the jail that are datasets of the project.
	WithVolumes bool
	// Zstd compresses the streams in the file.
	Zstd bool
}

// JailImportRequest are the options of an import.
type JailImportRequest struct {
	// As is the name of the jail on this host; empty keeps the exported name.
	As string `json:"as,omitempty"`
	// Project is the project to import into; empty is the project of the request.
	Project string `json:"project,omitempty"`
	// NetworkMap maps a network named in the file to a network of this host.
	NetworkMap map[string]string `json:"network_map,omitempty"`
	// VolumeMap maps a volume named in the file to a volume name in the project.
	VolumeMap map[string]string `json:"volume_map,omitempty"`
	// KeepIP keeps the addresses of the file when they are free here; otherwise the jail gets new ones.
	KeepIP bool `json:"keep_ip,omitempty"`
	// Start starts the jail when it is imported.
	Start bool `json:"start,omitempty"`
}

// JailImportPreflight is the body of POST /api/v1/jail-imports?dry_run=true: the manifest of a file and the options.
type JailImportPreflight struct {
	Manifest ExportManifest    `json:"manifest"`
	Request  JailImportRequest `json:"request"`
}

// JailImported is the reply of an import: the new jail and what the import did on the way.
type JailImported struct {
	Jail     Jail     `json:"jail"`
	Warnings []string `json:"warnings,omitempty"`
}
