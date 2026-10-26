// Package backup holds the host-side rules of the repository backup design
// (design section 9) that do not belong to the repository engine: the restore
// closure, the restore preflight and the restore test planner.
//
// Backups are encrypted, deduplicated snapshots in a chunk repository. Chunking,
// encryption, upload, retention and verification live in the repository
// packages; ZFS stream replication between trusted hosts lives in package
// replicate. This package is deliberately small and pure:
//
//   - BuildBundle freezes a jail's restore closure (api.RestoreBundle): the
//     spec, the image generation, and copies of the networks, firewall policy
//     and address sets it references, with their names.
//   - Preflight turns an api.RestoreRequest into an api.RestorePreflight: what
//     is missing, what would change, and what conflicts, honouring network_map,
//     keep_ip, as and replace. It changes nothing.
//   - PlanDrill, SelectExecutor and RunDrill plan and run restore tests at
//     three depths (deep, extract, full). A test needs the key, so it never runs
//     on the backup node, and it always cleans up through a reverter.
//
// Anything that touches the repository, the host's ZFS or its jails goes
// through the small interfaces Verifier, JailRunner and DrillEnv, so every
// rule here is tested without them.
package backup
