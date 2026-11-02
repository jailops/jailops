<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { s } from '@/i18n'
import { api } from '@/api/endpoints'
import { ApiError } from '@jailops/shared/api/client'
import { hasExt } from '@jailops/shared/composables/session'
import { projectState } from '@/composables/project'
import Icon from '@jailops/shared/components/Icon.vue'
import Callout from '@jailops/shared/components/Callout.vue'
import ErrorPanel from '@jailops/shared/components/ErrorPanel.vue'
import {
  applyUnified, emptyUnified, filterFromQuery, filterToQuery, jailHue, lineTime, sourceLabel, startUnified, unifiedQuery,
  MAX_TAIL, UNIFIED_FOLLOW_MS, UNIFIED_SINCE, type UnifiedFilter, type UnifiedState,
} from '@/lib/logs'
import type { Jail } from '@jailops/shared/api/types'

const route = useRoute()
const router = useRouter()
const available = computed(() => hasExt('logs_unified'))
const f = reactive<UnifiedFilter>(filterFromQuery(route.query, projectState.current))
// the text fields apply on Show, the choices at once
const draft = reactive({ grep: f.grep, file: f.file })
const jails = ref<Jail[]>([])
const st = ref<UnifiedState>(emptyUnified())
const error = ref<ApiError | null>(null)
const reading = ref(false)
const follow = ref(false)
const stopped = ref(false)
const hidden = ref(typeof document !== 'undefined' && document.hidden)
const wrap = ref(false)
const atEnd = ref(true)
const box = ref<HTMLElement | null>(null)
let timer: ReturnType<typeof setInterval> | undefined
let inflight = false
let gen = 0

const projectChoice = computed({
  get: () => (f.all ? '*' : f.project),
  set: (v: string) => { f.all = v === '*'; f.project = v === '*' ? '' : v },
})
const jailNames = computed(() => [...new Set(jails.value.map((j) => j.metadata.name))].sort())
const appNames = computed(() => [...new Set(jails.value.map((j) => j.metadata.labels?.['jailops.app']).filter((x): x is string => !!x))].sort())
// the jails of the lines, with their colour: a legend that also names what the colours mean
const legend = computed(() => [...new Set(st.value.lines.map((l) => sourceLabel(l, f.all)))].sort())
const hueOf = (label: string) => jailHue(label.split('/')[f.all ? 1 : 0])
const errOf = (e: unknown) => (e instanceof ApiError ? e : new ApiError({ message: String(e) }))

/** The jails to choose from: those of the chosen project, or of every project. */
async function loadJails(): Promise<void> {
  try {
    if (f.all) {
      const lists = await Promise.all(projectState.projects.map((p) => api.jails.list(p.metadata.name).catch(() => [] as Jail[])))
      jails.value = lists.flat()
    } else {
      jails.value = f.project ? await api.jails.list(f.project) : []
    }
  } catch {
    jails.value = []
  }
}

/** A fresh read with the filters: replaces what is shown. */
async function read(): Promise<void> {
  if (!available.value || (!f.all && !f.project)) return
  const mine = ++gen
  reading.value = true
  error.value = null
  try {
    const r = await api.logs.unified(unifiedQuery(f))
    if (mine !== gen) return
    st.value = startUnified(r)
    await nextTick()
    toEnd()
  } catch (e) {
    if (mine !== gen) return
    error.value = errOf(e)
    st.value = emptyUnified()
    follow.value = false
  } finally {
    if (mine === gen) reading.value = false
  }
}

/** One follow poll: what every source wrote since its cursor. Hidden tabs do not poll. */
async function poll(): Promise<void> {
  if (inflight || hidden.value || reading.value) return
  inflight = true
  const mine = gen
  try {
    const r = await api.logs.unified(unifiedQuery(f, st.value.next, MAX_TAIL))
    if (mine !== gen || !follow.value) return
    const end = atEnd.value
    st.value = applyUnified(st.value, r, end)
    if (end) {
      await nextTick()
      toEnd()
    }
  } catch (e) {
    error.value = errOf(e)
    follow.value = false
    stopped.value = true
  } finally {
    inflight = false
  }
}

function apply(): void {
  f.grep = draft.grep.trim()
  f.file = draft.file.trim() || 'messages'
}
function toggleJail(n: string): void {
  f.jails = f.jails.includes(n) ? f.jails.filter((x) => x !== n) : [...f.jails, n]
}
function toEnd(): void {
  const el = box.value
  if (el) el.scrollTop = el.scrollHeight
  atEnd.value = true
  st.value = { ...st.value, unseen: 0 }
}
function onScroll(): void {
  const el = box.value
  if (!el) return
  const end = el.scrollHeight - el.scrollTop - el.clientHeight < 24
  atEnd.value = end
  if (end && st.value.unseen) st.value = { ...st.value, unseen: 0 }
}
function onVisibility(): void {
  hidden.value = document.hidden
}

watch(follow, (on) => {
  clearInterval(timer)
  timer = undefined
  if (on) {
    stopped.value = false
    timer = setInterval(() => void poll(), UNIFIED_FOLLOW_MS)
  }
})
watch(() => [f.all, f.project], () => { f.jails = []; f.app = ''; void loadJails() })
watch(f, () => {
  void router.replace({ query: { ...(route.query.tab ? { tab: route.query.tab } : {}), ...filterToQuery(f, projectState.current) } })
  void read()
}, { deep: true })
// the first project choice may arrive after the page (the switcher loads the projects)
watch(() => projectState.current, (p) => { if (!f.all && !f.project && p) f.project = p })

onMounted(() => {
  document.addEventListener('visibilitychange', onVisibility)
  void loadJails()
  void read()
})
onBeforeUnmount(() => {
  clearInterval(timer)
  document.removeEventListener('visibilitychange', onVisibility)
  gen++
})
</script>

<template>
  <div data-test="ulogs">
    <Callout v-if="!available" tone="info">{{ s.ulogs.unavailable }}</Callout>
    <template v-else>
      <form class="panel ulogs-filters" :aria-label="s.ulogs.filters" @submit.prevent="apply">
        <div class="ulogs-row">
          <div class="ul-ctl"><label for="ul-project">{{ s.ulogs.project }}</label>
            <select id="ul-project" v-model="projectChoice" data-test="ul-project">
              <option value="*">{{ s.ulogs.allProjects }}</option>
              <option v-for="p in projectState.projects" :key="p.metadata.name" :value="p.metadata.name">{{ p.metadata.name }}</option>
              <option v-if="f.project && !projectState.projects.some((p) => p.metadata.name === f.project)" :value="f.project">{{ f.project }}</option>
            </select></div>
          <div class="ul-ctl"><label for="ul-app">{{ s.ulogs.app }}</label>
            <select id="ul-app" v-model="f.app" data-test="ul-app"><option value="">{{ s.ulogs.anyApp }}</option><option v-for="a in appNames" :key="a" :value="a">{{ a }}</option></select></div>
          <div class="ul-ctl"><label for="ul-since">{{ s.ulogs.since }}</label>
            <select id="ul-since" v-model="f.since" data-test="ul-since"><option v-for="x in UNIFIED_SINCE" :key="x" :value="x">{{ s.ulogs.sinceOpt[x] }}</option></select></div>
          <div class="ul-ctl ulogs-file"><label for="ul-file">{{ s.ulogs.file }}</label>
            <input id="ul-file" v-model="draft.file" class="mono" type="text" maxlength="512" :placeholder="s.ulogs.fileHint" autocomplete="off" data-test="ul-file" /></div>
          <div class="ul-ctl ul-grep"><label for="ul-grep">{{ s.ulogs.grep }}</label>
            <input id="ul-grep" v-model="draft.grep" type="search" maxlength="128" :placeholder="s.ulogs.grepHint" data-test="ul-grep" /></div>
          <button class="btn" type="submit" data-test="ul-apply"><Icon name="search" />{{ s.ulogs.apply }}</button>
        </div>
        <fieldset class="ulogs-jails">
          <legend>{{ s.ulogs.jails }} <span class="faint">{{ f.jails.length ? '' : `(${s.ulogs.allJails})` }}</span></legend>
          <button v-for="n in jailNames" :key="n" type="button" class="lchip lchip-btn" :style="{ '--h': jailHue(n) }" :aria-pressed="f.jails.includes(n)" :data-jail="n" @click="toggleJail(n)">{{ n }}</button>
          <span class="hint">{{ s.ulogs.jailsHint }}</span>
        </fieldset>
        <label class="check"><input v-model="f.appLogs" type="checkbox" data-test="ul-app-logs" />{{ s.ulogs.appLogs }}</label>
      </form>

      <section class="panel ulogs-viewer" aria-labelledby="ul-view-h">
        <header>
          <h2 id="ul-view-h">{{ s.ulogs.viewTab }}</h2>
          <span class="faint" data-test="ul-count">{{ s.ulogs.lines(st.lines.length) }} · {{ s.ulogs.sources(legend.length) }}</span>
          <span class="grow"></span>
          <div class="row ul-acts">
            <button class="btn" type="button" :aria-pressed="follow" :class="{ 'btn-primary': follow }" data-test="ul-follow" @click="follow = !follow"><Icon :name="follow ? 'pause' : 'play'" />{{ follow ? s.ulogs.following : s.ulogs.follow }}</button>
            <button class="btn btn-icon" type="button" :aria-pressed="wrap" :aria-label="s.ulogs.wrap" :title="s.ulogs.wrap" data-test="ul-wrap" @click="wrap = !wrap"><Icon name="wrapText" /></button>
          </div>
        </header>
        <div v-if="legend.length" class="ulogs-legend" data-test="ul-legend">
          <span v-for="x in legend" :key="x" class="lchip mono" :style="{ '--h': hueOf(x) }">{{ x }}</span>
        </div>
        <div v-if="st.failed.length" class="ulogs-failed" data-test="ul-failed">
          <Callout tone="warn">
            <strong>{{ s.ulogs.failedTitle }}</strong>
            <ul>
              <li v-for="x in st.failed" :key="x.key" :data-source="x.key"><span class="mono">{{ sourceLabel(x, f.all) }}</span>: {{ x.error?.message }}<span v-if="x.error?.hint" class="muted"> ({{ x.error.hint }})</span></li>
            </ul>
          </Callout>
        </div>
        <p v-if="st.truncated" class="faint ul-state" data-test="ul-truncated">{{ s.ulogs.truncated }}</p>
        <p v-if="follow && hidden" class="faint ul-state">{{ s.ulogs.paused }}</p>
        <ErrorPanel v-if="error" :error="error" class="ul-err" />
        <p v-if="stopped" class="faint ul-state">{{ s.ulogs.stoppedFollow }}</p>
        <div class="ul-box-wrap">
          <div ref="box" class="ul-box uul-box" :class="{ wrap }" role="log" aria-live="off" :aria-label="s.ulogs.region" :aria-busy="reading" tabindex="0" data-test="ul-view" @scroll="onScroll">
            <p v-if="!reading && !st.lines.length && !error" class="ul-none">{{ s.ulogs.noLines }}</p>
            <div v-for="ln in st.lines" :key="ln.id" class="ulog-line" :data-jail="ln.jail">
              <span class="ulog-time" :title="ln.time">{{ lineTime(ln.time) }}</span>
              <span class="lchip" :style="{ '--h': jailHue(ln.jail) }">{{ sourceLabel(ln, f.all) }}</span>
              <span class="ul-t">{{ ln.text }}</span>
            </div>
          </div>
          <button v-if="!atEnd" class="btn btn-sm ul-jump" type="button" data-test="ul-jump" @click="toEnd"><Icon name="arrowDownToLine" />{{ s.ulogs.jump }}<span v-if="st.unseen" class="ul-new">{{ s.ulogs.newLines(st.unseen) }}</span></button>
        </div>
      </section>
    </template>
  </div>
</template>

<style>
.ul-ctl { display: grid; gap: 2px; }
.ul-ctl label { font-size: 12px; color: var(--text-2); font-weight: 500; }
.ul-grep { flex: 1 1 160px; max-width: 280px; }
.ul-acts { gap: 6px; }
.ul-state { margin: 6px 12px 0; font-size: 12px; }
.ul-err { margin: 8px 12px 0; }
.ul-box-wrap { position: relative; margin-top: 8px; }
.ul-box { height: min(62vh, 600px); overflow: auto; padding: 8px 0; font-family: var(--font-mono); font-size: 12.5px; line-height: 1.6; background: var(--surface-2); border-radius: 0 0 var(--radius-lg) var(--radius-lg); tab-size: 8; }
.ul-box:focus-visible { outline-offset: -2px; }
.ul-none { padding: 24px 12px; text-align: center; color: var(--text-2); font-family: var(--font-sans); }
.ul-jump { position: absolute; right: 16px; bottom: 12px; box-shadow: var(--shadow); }
.ul-new { margin-left: 4px; padding: 0 6px; border-radius: 999px; background: var(--accent); color: var(--accent-text); font-size: 12px; }
.ulogs-filters { padding: 8px 12px; display: grid; gap: 8px; margin-bottom: 12px; }
.ulogs-row { display: flex; flex-wrap: wrap; gap: 8px; align-items: flex-end; }
.ulogs-row .ul-ctl select { width: auto; max-width: 100%; }
.ulogs-file { flex: 0 1 180px; }
.ulogs-jails { border: 1px solid var(--border); border-radius: var(--radius); padding: 6px 10px; margin: 0; display: flex; flex-wrap: wrap; gap: 6px; align-items: center; }
.ulogs-jails legend { padding: 0 4px; color: var(--text-2); font-size: 12px; font-weight: 500; }
.ulogs-jails .hint { flex-basis: 100%; margin: 0; }
.ulogs-viewer > header { flex-wrap: wrap; gap: 8px; }
.ulogs-legend { display: flex; flex-wrap: wrap; gap: 6px; padding: 8px 12px 0; }
.ulogs-failed { padding: 8px 12px 0; }
.ulogs-failed ul { margin: 4px 0 0; padding-left: 18px; }
/* A jail's chip: the colour marks the jail (a border, so the text keeps the page's contrast); the text names it. */
.lchip { --chip: hsl(var(--h, 212) 62% 38%); display: inline-flex; align-items: center; flex: none; padding: 0 6px; border: 1px solid var(--border); border-left: 4px solid var(--chip); border-radius: var(--radius); background: var(--surface); color: var(--text); font-family: var(--font-mono); font-size: 12px; line-height: 1.6; white-space: nowrap; }
.lchip-btn { cursor: pointer; font-size: 13px; padding: 2px 8px; }
.lchip-btn[aria-pressed='true'] { background: var(--accent-soft); border-color: var(--accent); border-left-color: var(--chip); font-weight: 600; }
@media (prefers-color-scheme: dark) { :root:not([data-theme='light']) .lchip { --chip: hsl(var(--h, 212) 70% 66%); } }
:root[data-theme='dark'] .lchip { --chip: hsl(var(--h, 212) 70% 66%); }
.ulog-line { display: flex; gap: 10px; padding: 0 12px; white-space: pre; min-width: max-content; align-items: baseline; }
.uul-box.wrap .ulog-line { white-space: pre-wrap; overflow-wrap: anywhere; min-width: 0; flex-wrap: wrap; }
.ulog-line:hover { background: var(--surface-3); }
.ulog-time { flex: none; min-width: 19ch; color: var(--text-2); user-select: none; }
@media (max-width: 700px) { .ulog-line { flex-wrap: wrap; white-space: pre-wrap; overflow-wrap: anywhere; min-width: 0; gap: 2px 8px; } .ulog-time { min-width: 0; } .ulog-line .ul-t { flex-basis: 100%; } }
</style>
