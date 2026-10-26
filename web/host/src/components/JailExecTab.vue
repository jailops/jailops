<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, shallowRef } from 'vue'
import { s } from '@/i18n'
import { api } from '@/api/endpoints'
import { hasExt } from '@jailops/shared/composables/session'
import { ApiError } from '@jailops/shared/api/client'
import type { EventStream } from '@jailops/shared/api/client'
import Callout from '@jailops/shared/components/Callout.vue'
import ErrorPanel from '@jailops/shared/components/ErrorPanel.vue'
import { Screen, keyToBytes, type Cell } from '@/lib/vt100'
import type { Jail } from '@jailops/shared/api/types'

const props = defineProps<{ jail: Jail }>()
const name = computed(() => props.jail.metadata.name)
const available = computed(() => hasExt('jail_console'))
const running = computed(() => props.jail.status.power === 'running')
const shell = computed(() => (props.jail.spec as unknown as { shell?: string }).shell || '/bin/sh')

const screen = shallowRef(new Screen(80, 24))
const tick = ref(0) // bumps on every write so the view re-renders
const state = reactive({ phase: 'idle' as 'idle' | 'connecting' | 'open' | 'closed', exit: undefined as number | undefined })
const error = ref<ApiError | null>(null)
const copied = ref(false)
const term = ref<HTMLElement | null>(null)
const probe = ref<HTMLElement | null>(null)
let sessionId = ''
let stream: EventStream | null = null
let ro: ResizeObserver | null = null
let pending = ''
let flush: ReturnType<typeof setTimeout> | undefined

const palette = ['#000', '#c91b00', '#00c200', '#c7c400', '#2225c4', '#c930c7', '#00c5c7', '#c7c7c7', '#686868', '#ff6e67', '#5ffa68', '#fffc67', '#6871ff', '#ff77ff', '#60fdff', '#fff']
const color = (n: number) => (n < 0 ? undefined : n < 16 ? palette[n] : undefined)

interface Run { text: string; style: Record<string, string> }
const lines = computed<Run[][]>(() => {
  void tick.value
  const sc = screen.value
  return sc.grid.map((row, y) => {
    const runs: Run[] = []
    let cur: Run | null = null
    let key = ''
    row.forEach((c: Cell, x) => {
      const cursor = state.phase === 'open' && y === sc.y && x === sc.x
      const k = `${c.fg}|${c.bg}|${c.bold}|${c.ul}|${c.rev}|${cursor}`
      if (!cur || k !== key) {
        const style: Record<string, string> = {}
        let fg = color(c.fg), bg = color(c.bg)
        if (c.rev || cursor) { [fg, bg] = [bg ?? 'var(--surface)', fg ?? 'var(--text)'] }
        if (fg) style.color = fg
        if (bg) style.background = bg
        if (c.bold) style.fontWeight = '600'
        if (c.ul) style.textDecoration = 'underline'
        cur = { text: '', style }
        key = k
        runs.push(cur)
      }
      cur.text += c.ch
    })
    return runs
  })
})

function write(bytes: Uint8Array | string): void {
  screen.value.write(bytes)
  tick.value++
}
function schedule(): void {
  if (flush) return
  flush = setTimeout(() => {
    flush = undefined
    const chunk = pending
    pending = ''
    void api.exec.input(name.value, sessionId, chunk).catch((e) => (error.value = e instanceof ApiError ? e : null))
  }, 8)
}
function send(data: string): void {
  if (state.phase !== 'open') return
  pending += data
  schedule()
}

function measure(): { cols: number; rows: number } {
  const el = term.value
  const p = probe.value
  if (!el || !p) return { cols: 80, rows: 24 }
  const r = p.getBoundingClientRect()
  const cw = r.width / 10 || 8
  const ch = r.height || 16
  return { cols: Math.max(20, Math.floor((el.clientWidth - 16) / cw)), rows: Math.max(5, Math.floor(Math.min(el.clientHeight || 400, 480) / ch)) }
}
let lastSize = ''
function fit(): void {
  const { cols, rows } = measure()
  const key = `${cols}x${rows}`
  if (key === lastSize) return
  lastSize = key
  screen.value.resize(cols, rows)
  tick.value++
  if (state.phase === 'open') void api.exec.resize(name.value, sessionId, cols, rows).catch(() => undefined)
}

async function connect(): Promise<void> {
  error.value = null
  state.phase = 'connecting'
  state.exit = undefined
  await nextTick()
  lastSize = ''
  screen.value = new Screen(80, 24)
  fit()
  const { cols, rows } = measure()
  try {
    const r = await api.exec.open(name.value, { shell: true, tty: true, cols, rows })
    sessionId = (r.kind === 'sync' ? (r.data as { id: string }).id : '') || ''
    state.phase = 'open'
    stream = api.exec.output(name.value, sessionId, {
      onEvent: (e) => {
        const m = e.metadata as { data?: string; exit?: number }
        if (m?.data) write(Uint8Array.from(atob(m.data), (c) => c.charCodeAt(0)))
        if (m?.exit !== undefined) end(m.exit)
      },
      onError: () => end(undefined),
    })
    term.value?.focus()
  } catch (e) {
    state.phase = 'closed'
    error.value = e instanceof ApiError ? e : new ApiError({ message: String(e) })
  }
}
function end(code: number | undefined): void {
  if (state.phase === 'closed') return
  state.phase = 'closed'
  state.exit = code
  stream?.close()
  stream = null
}
async function disconnect(): Promise<void> {
  const id = sessionId
  end(undefined)
  if (id) await api.exec.close(name.value, id).catch(() => undefined)
}

function onKey(e: KeyboardEvent): void {
  if (state.phase !== 'open') return
  // Ctrl+Shift+C and Ctrl+Shift+V are the browser's copy and paste; plain Ctrl+C goes to the jail.
  if (e.ctrlKey && e.shiftKey && (e.key === 'C' || e.key === 'V')) return
  const b = keyToBytes(e)
  if (b === null) return
  e.preventDefault()
  send(b)
}
function onPaste(e: ClipboardEvent): void {
  const t = e.clipboardData?.getData('text')
  if (t) {
    e.preventDefault()
    send(t.replace(/\r?\n/g, '\r'))
  }
}
async function paste(): Promise<void> {
  try {
    send((await navigator.clipboard.readText()).replace(/\r?\n/g, '\r'))
  } catch {
    term.value?.focus()
  }
}
async function copy(): Promise<void> {
  const text = screen.value.allText()
  try {
    await navigator.clipboard.writeText(text)
  } catch {
    /* clipboard blocked: the selection still works by hand */
  }
  copied.value = true
  setTimeout(() => (copied.value = false), 1500)
}

onMounted(() => {
  if (term.value && typeof ResizeObserver !== 'undefined') {
    ro = new ResizeObserver(() => fit())
    ro.observe(term.value)
  }
})
onBeforeUnmount(() => {
  ro?.disconnect()
  stream?.close()
  if (state.phase === 'open' && sessionId) void api.exec.close(name.value, sessionId).catch(() => undefined)
})
defineExpose({ screen, write })
</script>

<template>
  <div class="stack" data-test="exec-tab">
    <div class="row"><span class="muted">{{ s.exec.lead }}</span></div>
    <Callout v-if="!available" tone="warn" data-test="exec-unavailable"><strong>{{ s.exec.unavailableTitle }}</strong><br />{{ s.exec.unavailableText }}</Callout>
    <template v-else>
      <Callout v-if="!running">{{ s.exec.notRunning }}</Callout>
      <div class="row">
        <button v-if="state.phase !== 'open' && state.phase !== 'connecting'" class="btn btn-primary" type="button" :disabled="!running" data-test="exec-open" @click="connect">{{ s.exec.connect }}</button>
        <button v-else class="btn" type="button" data-test="exec-close" @click="disconnect">{{ s.exec.disconnect }}</button>
        <button class="btn" type="button" :disabled="state.phase !== 'open'" @click="paste">{{ s.exec.paste }}</button>
        <button class="btn" type="button" data-test="exec-copy" @click="copy">{{ copied ? s.exec.copied : s.exec.copy }}</button>
        <span class="faint mono">{{ shell }} · {{ s.exec.size(screen.cols, screen.rows) }}</span>
        <span v-if="state.phase === 'connecting'" class="pill info pulse">{{ s.exec.connecting }}</span>
        <span v-else-if="state.phase === 'open'" class="pill ok">{{ s.exec.connected }}</span>
        <span v-else-if="state.phase === 'closed'" class="pill">{{ s.exec.closed(state.exit) }}</span>
      </div>
      <ErrorPanel v-if="error" :error="error" />
    </template>
    <div ref="term" class="term" tabindex="0" role="textbox" aria-multiline="true" :aria-label="s.exec.terminal" data-test="terminal" @keydown="onKey" @paste="onPaste">
      <span ref="probe" class="term-probe" aria-hidden="true">MMMMMMMMMM</span>
      <pre class="term-screen"><div v-for="(runs, y) in lines" :key="y" class="term-line"><span v-for="(r, i) in runs" :key="i" :style="r.style">{{ r.text }}</span></div></pre>
    </div>
    <p class="faint">{{ s.exec.hint }}</p>
  </div>
</template>

<style>
.term { position: relative; background: #0b0f17; color: #d7dde8; border: 1px solid var(--border-strong); border-radius: var(--radius); padding: 8px; height: 420px; overflow: auto; font-family: var(--font-mono); font-size: 13px; line-height: 1.25; }
.term:focus { outline: 2px solid var(--focus); outline-offset: 1px; }
.term-screen { margin: 0; font: inherit; white-space: pre; }
.term-line { min-height: 1.25em; }
.term-probe { position: absolute; visibility: hidden; white-space: pre; font: inherit; }
</style>
