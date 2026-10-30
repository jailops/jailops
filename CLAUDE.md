# jailops: working notes

JailOps is a FreeBSD jails-only platform, ZFS-native, pure Go, with no dependency on any other management platform.
Module `jailops.net/jailops` with a separate module `jailops.net/jailops/sdk`.
Go 1.27, `CGO_ENABLED=0`, BSD-2-Clause. The developer environment is Linux; FreeBSD is
reached only through `GOOS=freebsd` cross builds and a FreeBSD VM for integration tests.

## Ground rules
- `docs/design.md` is the contract. If code and design disagree, fix one in the same change.
- Do not fake. Anything that needs FreeBSD and has not run there is marked `UNVERIFIED:` in a
  comment and listed in design section 16. A feature that is not implemented returns
  `api.CodeUnsupported`; it never returns success.
- JailOps stands alone. Do not name other projects in code, comments, tests, commit messages or
  errors (no "like X", no "as in Y"); describe the behaviour and the reason instead. The one exception is
  the acknowledgement in LICENSE.md, README.md and docs/design.md that the backup repository and `jailops-sync` build on
  `incus-backup` and `incus-sync`, earlier projects by the same author. Code from those two may be adopted
  and adapted; no origin comments or license headers are left in source files (the LICENSE.md acknowledgement covers it). Code from anyone else is never
  copied; designs are reused, code is written here.
- ZFS-native only. No nullfs thin layout, no alternative storage backends.
- Desired state never contains observed results. Reconcile observes, renders (pure, golden-tested),
  plans, applies under per-resource locks, verifies. Failures go to `status.conditions`.
- All external commands run through `internal/sys/run` (argv only, never a shell, always with a
  timeout) so tests substitute fakes. `zfs` and `zpool` use `-H -p`.
- Every config value that reaches `jail.conf`, `rc.conf`, an interface file or PF is escaped by the
  renderer. Renderers are pure functions with golden tests.
- No business logic in HTTP handlers. Handlers call `internal/service`.
- Every route has an entry in `internal/auth/perms.go`; a route without one makes the daemon
  refuse to start. State changes are audited. Secrets are shown once and never returned.
- The backup node's control plane is PostgreSQL (pgx/v5) and nothing else; the per-host daemon uses SQLite.
  Backup-node tests use `JAILOPS_TEST_DATABASE_URL`, else embedded-postgres, and FAIL (never skip) when neither works.
- `cmd/jailops-backupd` (the backup node) is keyless and separate: it links no jail, ZFS, privileged-helper or
  decryption code (enforced by a test). Every web UI is its own app under web/<app> (registry: web/apps.json),
  belongs to exactly one binary and one API role, may import only web/shared and may call only its registered URL
  prefixes (enforced by tests). More apps will follow: add them with the scaffold script, never by extending another app.
- `cmd/jailops` and `cmd/jailops-sync` import only `sdk` and `sdk/api` (enforced by
  `internal/archtest`). Exceptions: `internal/manpage`, and `internal/apps` in the client for `app validate`
  (kept free of daemon code by `internal/cli/arch_test.go`).
- Never delete or modify objects JailOps does not own (`jailops:managed=on`, `managed_by`).
- Error codes in `sdk/api/errors.go` are stable; add, never rename.
- Memory is bounded: streams are streamed, never read whole into memory.
- Tests fail, never skip, when an integration prerequisite is missing in an integration run.

## Layout
`sdk/api` wire types · `sdk` client · `sdk/fake` · `cmd/{jailops,jailopsd,jailops-priv,jailops-sync,jailops-backupd}` ·
`internal/{service,state,store,render,reconcile,ndproxy,backup,auth,archtest,webui}` ·
`internal/sys/{run,zfs,jailos,pfctl,ifconfig}` · `web/` (Vue 3) · `docs/`.

## Commands
`just build` · `just test` · `just test-freebsd` (on a FreeBSD host) · `just fmt` · `just check` ·
`just build-freebsd` · `just fuzz`
