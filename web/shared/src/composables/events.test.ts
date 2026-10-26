import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { backoff, startEvents, stopEvents, eventState } from './events'
import { Client, setClient, type StreamHandlers, type Transport } from '@jailops/shared/api/client'

describe('events', () => {
  let handlers: StreamHandlers[] = []
  beforeEach(() => {
    vi.useFakeTimers()
    handlers = []
    const t: Transport = { request: async () => ({ status: 200, envelope: { type: 'sync' } }), stream: (_p, h) => (handlers.push(h), { close() {} }) }
    setClient(new Client(t))
  })
  afterEach(() => {
    stopEvents()
    vi.useRealTimers()
  })
  it('backoff grows and caps', () => {
    expect(backoff(0, () => 0)).toBe(1000)
    expect(backoff(3, () => 0)).toBe(8000)
    expect(backoff(20, () => 0)).toBe(30000)
  })
  it('goes live, collects events, reconnects after an error', () => {
    startEvents()
    expect(handlers).toHaveLength(1)
    handlers[0].onOpen?.()
    expect(eventState.link).toBe('live')
    handlers[0].onEvent({ type: 'lifecycle', timestamp: 't', metadata: {} })
    expect(eventState.items[0].type).toBe('lifecycle')
    handlers[0].onError?.()
    expect(eventState.link).toBe('reconnecting')
    vi.advanceTimersByTime(1300)
    expect(handlers).toHaveLength(2)
  })
})
