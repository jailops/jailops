package replicate

import (
	"context"
	"io"
	"regexp"
	"time"

	"jailops.net/jailops/sdk/api"
)

// SnapInfo describes a snapshot or a bookmark. Name is the short name, without
// the dataset and without the '@' or '#'.
type SnapInfo struct {
	Name    string
	GUID    uint64
	Created time.Time
}

// Stream is a send stream. Size is the declared size in bytes of this stream
// (zfs send -nP), or -1 when unknown.
type Stream struct {
	io.ReadCloser
	Size int64
}

// SendOpts selects one zfs send.
type SendOpts struct {
	Dataset  string
	Snapshot string
	// From is the incremental base; empty for a full send.
	From         string
	FromBookmark bool
	Raw          bool
	// ResumeToken, when set, resumes an interrupted receive (zfs send -t).
	ResumeToken string
}

// RecvOpts selects one zfs receive. There is deliberately no force option.
// Receives are always resumable (-s).
type RecvOpts struct {
	Dataset string
}

// RecvResult is what a completed receive produced.
type RecvResult struct {
	Snapshot string
	GUID     uint64
	// Bytes consumed from the stream by this call.
	Bytes int64
}

// ResumeInfo describes an interrupted receive that can continue.
type ResumeInfo struct {
	Token string
	// FromGUID is zero for a full stream.
	FromGUID, ToGUID uint64
	// BytesReceived is how much of the stream the receiver already holds.
	BytesReceived int64
}

// ZFS is the narrow ZFS surface this package needs.
type ZFS interface {
	Exists(ctx context.Context, dataset string) (bool, error)
	Snapshots(ctx context.Context, dataset string) ([]SnapInfo, error) // oldest first
	Bookmarks(ctx context.Context, dataset string) ([]SnapInfo, error)
	Holds(ctx context.Context, dataset, snapshot string) ([]string, error)
	Hold(ctx context.Context, dataset, snapshot, tag string) error
	Release(ctx context.Context, dataset, snapshot, tag string) error
	CreateBookmark(ctx context.Context, dataset, snapshot, bookmark string) error
	DestroyBookmark(ctx context.Context, dataset, bookmark string) error
	// DestroySnapshot fails with a busy error while any hold exists.
	DestroySnapshot(ctx context.Context, dataset, snapshot string) error
	GetProp(ctx context.Context, dataset, prop string) (string, error)
	SetProp(ctx context.Context, dataset, prop, value string) error
	Send(ctx context.Context, o SendOpts) (Stream, error)
	// Receive never forces. An interrupted stream returns an error and leaves
	// resumable state visible through ResumeInfo.
	Receive(ctx context.Context, o RecvOpts, r io.Reader) (RecvResult, error)
	ResumeInfo(ctx context.Context, dataset string) (*ResumeInfo, error)
	// AbortResume discards partial receive state only (zfs receive -A).
	AbortResume(ctx context.Context, dataset string) error
}

// Hold tags and property names.
const (
	TagKeep       = "jailops-keep"       // retained snapshot
	TagLast       = "jailops-last"       // last common snapshot of a chain
	TagQuarantine = "jailops-quarantine" // received snapshot that failed verification
	PropManaged   = "jailops:managed"
	PropChain     = "jailops:chain"
)

// RoleReplicaAdmin is the role that may request and execute deletions.
const RoleReplicaAdmin = "replica-admin"

var idRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,63}$`)

// ValidID reports whether s is safe as a chain id, host uuid or snapshot name
// component of a ZFS name.
func ValidID(s string) bool { return idRe.MatchString(s) }

func invalid(format string, a ...any) error { return api.Errorf(api.CodeInvalid, format, a...) }
