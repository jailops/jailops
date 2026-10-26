// Package replicate moves jail datasets between hosts that trust each other,
// as ZFS send streams, for cold migration and for a warm standby (design
// section 9.8). It is not backup: a compromised source host can corrupt a
// replica, which is why backup is a separate, keyless repository.
//
// The package covers the chain model with guid continuity and fork detection,
// transfer planning (full or incremental, source bookmarks, hold and release
// ordering, resume tokens), Pull and Push orchestration over Sender and
// Receiver, the receiving side (Target) and the ZFS feature compatibility
// pre-check hook. Raw sends keep replicas of encrypted datasets encrypted at
// rest; the key must be loaded on the receiving host to run the jail, and
// KeyStatus records whether it is escrowed.
//
// The package talks to ZFS only through the narrow ZFS interface, so every rule
// here is tested against an in-memory model. Behaviour of the real zfs(8) that
// the rules rely on and that has not run on FreeBSD is marked UNVERIFIED.
//
// Protection model:
//   - The receiver never forces a receive. RecvOpts has no force field, so a
//     stream cannot roll back or overwrite existing snapshots.
//   - Hosts talk to a Target only through the Receiver interface, which has no
//     destroy, release or rollback operation. A Target binds each Receiver to
//     the authenticated host identity and derives the target dataset itself.
//   - Retained snapshots and the last common snapshot of every chain carry zfs
//     holds. Only the retention engine (Target.ApplyRetention) deletes, plus the
//     two-step admin path (RequestDeletion, then ExecuteDeletion after a delay).
package replicate
