import type { JailRollback, JailSnapshot, RollbackScope } from '@jailops/shared/api/types'

/** Data and all discard everything written since the snapshot, so they need the jail name typed. */
export function needsConfirm(scope: RollbackScope): boolean {
  return scope === 'data' || scope === 'all'
}

export function canSubmit(scope: RollbackScope, typed: string, jail: string, snapshot: string | undefined): boolean {
  if (needsConfirm(scope)) return !!snapshot && typed === jail
  return true
}

/** Bytes a rollback of this scope would throw away. Root rollbacks keep data. */
export function bytesAtStake(scope: RollbackScope, snap: JailSnapshot | undefined): number {
  return needsConfirm(scope) ? (snap?.status.written ?? 0) : 0
}

export function request(scope: RollbackScope, snapshot: string | undefined): JailRollback {
  return { scope, ...(snapshot ? { snapshot } : {}), confirm_data_loss: needsConfirm(scope) }
}
