// Package node is the keyless backup-node service as a library plus HTTP
// handlers. It owns a backup repository on a datastore.Backend (a directory or
// S3) and a PostgreSQL control plane (internal/node/ctl).
//
// The node holds no key and never decrypts anything. It can verify what it
// stores only through unkeyed checksums: the SHA-256 trailers of chunk blobs,
// indexes and manifests, and the digests in commit receipts. Its trust model:
//
//   - A host may check for chunks, upload chunks, an index and a manifest, and
//     ask for a commit. Every write is create-only; there is no delete and no
//     overwrite on the host scope. The node, not the host, writes the commit
//     receipt (commit.json), last, so a host cannot forge a receipt or a date.
//   - Sessions are leases (15 minutes, renewed by activity), one open session
//     per jail group, held in PostgreSQL. A session pins every chunk it saw
//     present or uploaded, and the previous snapshot, until it closes.
//   - Everything destructive is administration: retention, garbage collection,
//     quarantine, deletion, maintenance modes. It is a Go API and a set of
//     handlers (AdminHandler) that the process serves on a local unix socket
//     only, never on the network listener.
//
// Repository objects (same key space on every backend):
//
//	datastore.json                       format version, id, created        create-only
//	owner.json                           the node that owns the repository  create-only
//	chunks/aa/bb/<id>                    one encrypted chunk blob           write-once
//	quarantine/<id>                      a corrupt blob moved out of chunks/
//	escrow/...                           key escrow blobs written by hosts' tooling
//	groups/<group>/owner.json            the host (by enrolled name) that owns the group; create-only
//	groups/<group>/<snap>/index          plaintext (end offset, chunk id) list with a trailer
//	groups/<group>/<snap>/manifest       encrypted manifest with a trailer
//	groups/<group>/<snap>/commit.json    receipt, written last, create-only
//	groups/<group>/<snap>/protected      marker: retention and deletion refuse
//	groups/<group>/<snap>/deletion.json  a pending deletion request
//	groups/<group>/<snap>/verify.json    verification history
//
// A snapshot exists if and only if its commit.json exists.
package node
