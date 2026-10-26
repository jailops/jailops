// Calls every console needs, whatever the server role: server info and the browser session.
import { getClient } from './client'
import type { LoginRequest, ServerInfo, SessionInfo } from './types'

const c = () => getClient()

export const core = {
  server: () => c().get<ServerInfo>(''),
  /** POST /1.0/auth/login: sets the session cookie and returns the user, role and CSRF token. */
  login: (r: LoginRequest) => c().mutate<SessionInfo>('POST', '/auth/login', r),
  logout: () => c().mutate('POST', '/auth/logout'),
  session: () => c().get<SessionInfo>('/auth/session'),
}
