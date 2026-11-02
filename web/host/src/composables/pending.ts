// The change that waits for its confirmation (commit confirmed), shared by the banner and the pages that make such
// changes. The banner polls; a page that applied a change asks for a fresh read at once.
import { reactive } from 'vue'
import { api } from '@/api/endpoints'
import type { HostPending } from '@jailops/shared/api/types'

export const pendingState = reactive({
  data: null as HostPending | null,
  /** Date.now() when data was read: the countdown runs from the server's seconds_left at that moment. */
  fetchedAt: 0,
  /** Bumped after a confirm or a revert so pages reload what changed. */
  generation: 0,
})

let inflight: Promise<void> | null = null

export function refreshPending(): Promise<void> {
  inflight ??= (async () => {
    try {
      const d = await api.host.pending()
      pendingState.data = d
      pendingState.fetchedAt = Date.now()
    } catch {
      // a failed read keeps what was shown; the next poll tries again
    } finally {
      inflight = null
    }
  })()
  return inflight
}

export function setPending(d: HostPending): void {
  pendingState.data = d
  pendingState.fetchedAt = Date.now()
  pendingState.generation++
}
