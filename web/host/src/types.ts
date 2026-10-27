// Host-console types that sdk/api does not define.
export * from '@jailops/shared/api/types'

/**
 * A dry-run upgrade preview. sdk/PROTOCOL.md says upgrade is async with result Jail and does not
 * define a dry-run body: this is the shape the console expects in the operation metadata.
 */
export interface UpgradePreview {
  from_generation: string
  to_generation: string
  steps: string[]
  etc_conflicts?: string[]
  warnings?: string[]
}

/** Body of POST /api/v1/jails/{name}/snapshots: a JailSnapshot with an optional name. */
export interface SnapshotBody {
  metadata: { name?: string }
  spec: { jail: string; comment?: string }
}

// ---- Console-side shapes -------------------------------------------------------------------------

/** What the browser can use of exec: the daemon offers only an HTTP upgrade (sdk/PROTOCOL.md, Exec), which
 *  a browser cannot perform. The console talks to a bridge route that is not part of the protocol yet. */
export interface ExecSessionInfo { id: string }

/** The result of POST /jails/{n}/config: the jail plus what needs a restart and whether it was restarted. */
export interface ConfigResult { restart_required?: string[]; restarted?: boolean }
