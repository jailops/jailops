import type { NodeRole } from '@/types'

const rank: Record<string, number> = { readonly: 0, 'backup-admin': 1, admin: 2 }

/** Whether the signed-in role satisfies a required one. An unknown role (a token without a session)
 *  is allowed to try: the server is the authority and answers `forbidden`. */
export function allows(have: string, need: NodeRole): boolean {
  if (!(have in rank)) return true
  return rank[have] >= rank[need]
}

/** The roles that may do something, for display next to the control. */
export function who(need: NodeRole): string {
  return need === 'admin' ? 'admin' : need === 'backup-admin' ? 'backup-admin or admin' : 'any role'
}
