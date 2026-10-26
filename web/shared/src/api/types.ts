// Hand-written additions to the generated wire types (types.gen.ts, from sdk/api).
import type { Operation, Error as ApiErrorBody, Event as ApiEvent } from './types.gen'

export * from './types.gen'
export type { Operation, ApiEvent, ApiErrorBody }

/** A web-session login (POST /1.0/auth/login). The backup node ignores totp. */
export interface LoginRequest {
  username: string
  password: string
  totp?: string
}

/** The result of a mutation: a finished sync reply or a started operation. */
export type MutationResult<T = unknown> =
  | { kind: 'sync'; data: T; etag?: string }
  | { kind: 'async'; op: Operation }

/** The reply of login and GET /auth/session. The host daemon says `permission`, the backup node `role`. */
export interface SessionInfo {
  user: string
  role?: string
  permission?: string
  csrf?: string
  expires_at?: string
  /** Identity fields of the host daemon (GET /auth/session). */
  kind?: string
  projects?: string[]
  global?: boolean
}
