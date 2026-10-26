import { reactive, ref } from 'vue'
import { getClient, type EventStream } from '@jailops/shared/api/client'
import type { ApiEvent } from '@jailops/shared/api/types'

export type Link = 'idle' | 'connecting' | 'live' | 'reconnecting'

const MAX = 500

export const eventState = reactive({
  link: 'idle' as Link,
  items: [] as ApiEvent[],
  retryIn: 0,
})

/** Bumped whenever the server says something changed, so views reload their data. */
export const dataVersion = ref(0)

let stream: EventStream | null = null
let timer: ReturnType<typeof setTimeout> | undefined
let attempt = 0
let wanted = false
let bumpTimer: ReturnType<typeof setTimeout> | undefined
const listeners = new Set<(e: ApiEvent) => void>()

export function onEvent(fn: (e: ApiEvent) => void): () => void {
  listeners.add(fn)
  return () => listeners.delete(fn)
}

/** Backoff 1s, 2s, 4s ... capped at 30s, with up to 20 percent jitter. */
export function backoff(n: number, rand = Math.random): number {
  const base = Math.min(30_000, 1000 * 2 ** Math.max(0, n))
  return Math.round(base * (1 + rand() * 0.2))
}

function bump(): void {
  clearTimeout(bumpTimer)
  bumpTimer = setTimeout(() => {
    dataVersion.value++
  }, 250)
}

function push(e: ApiEvent): void {
  eventState.items.unshift(e)
  if (eventState.items.length > MAX) eventState.items.length = MAX
  listeners.forEach((f) => f(e))
  bump()
}

function open(): void {
  eventState.link = attempt === 0 ? 'connecting' : 'reconnecting'
  stream = getClient().events({
    onOpen: () => {
      attempt = 0
      eventState.link = 'live'
      eventState.retryIn = 0
      // A reconnect may have missed events: reload everything once.
      bump()
    },
    onEvent: push,
    onError: () => {
      stream?.close()
      stream = null
      if (!wanted) return
      const wait = backoff(attempt++)
      eventState.link = 'reconnecting'
      eventState.retryIn = wait
      timer = setTimeout(() => {
        if (wanted) open()
      }, wait)
    },
  })
}

export function startEvents(): void {
  if (wanted) return
  wanted = true
  attempt = 0
  open()
}

export function stopEvents(): void {
  wanted = false
  clearTimeout(timer)
  stream?.close()
  stream = null
  eventState.link = 'idle'
}
