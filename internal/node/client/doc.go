// Package client talks to a backup node over HTTPS with mutual TLS and a pinned
// server certificate. It uses only net/http and internal/auth. It holds no
// repository key and does no encryption: the host engine encrypts, this
// package only moves opaque blobs.
//
// # The interface the host engine adopts
//
// Two small interfaces describe everything a host needs of a backup target.
// A target that is a local directory implements the same interfaces over a
// datastore.Backend, so the engine does not care which it has.
//
//	type Uploader interface {
//		Begin(ctx, group, snapshot, previous string) (UploadSession, error)
//	}
//
//	type UploadSession interface {
//		ID() string
//		Previous() string                                    // "" when the node has no such snapshot
//		Missing(ctx, ids []format.ID) ([]format.ID, error)   // up to 2000 per call; present ones are pinned
//		PutChunk(ctx, id format.ID, blob []byte) error       // create-only; "already exists" is success
//		PutIndex(ctx, index []byte) error
//		PutManifest(ctx, manifest []byte) error
//		Commit(ctx) (*format.Receipt, error)                 // idempotent; ErrMissingChunks lists what to upload
//		Abort(ctx) error
//	}
//
//	type Reader interface {
//		Snapshots(ctx, group string) ([]Snapshot, error)
//		Receipt(ctx, group, snapshot string) (*format.Receipt, error)
//		Index(ctx, group, snapshot string, off, length int64) (io.ReadCloser, int64, error) // range; total size
//		Manifest(ctx, group, snapshot string) ([]byte, error)
//		Chunk(ctx, id format.ID) ([]byte, error)
//	}
//
// An UploadSession keeps its lease alive by itself (a heartbeat at a third of
// the lease) until Commit or Abort. Calls are safe for concurrent use; the
// engine uploads chunks in parallel. A node in maintenance answers with
// api.CodeMaintenance: the engine defers the run, it is not a failure.
package client
