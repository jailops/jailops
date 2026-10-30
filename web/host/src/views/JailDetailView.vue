<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { s } from '@/i18n'
import { api } from '@/api/endpoints'
import { ApiError } from '@jailops/shared/api/client'
import { useLoad } from '@jailops/shared/composables/load'
import { can, hasExt } from '@jailops/shared/composables/session'
import { lifecycle, type Lifecycle } from '@/composables/jailActions'
import { runOp } from '@jailops/shared/composables/ops'
import { bytes, stamp, ago } from '@jailops/shared/lib/format'
import StatePill from '@/components/StatePill.vue'
import Icon from '@jailops/shared/components/Icon.vue'
import Callout from '@jailops/shared/components/Callout.vue'
import LoadState from '@jailops/shared/components/LoadState.vue'
import RollbackDialog from '@/components/RollbackDialog.vue'
import UpgradeDialog from '@/components/UpgradeDialog.vue'
import { sourcesOf, flatten } from '@/lib/configKeys'
import JailConfigTab from '@/components/JailConfigTab.vue'
import JailDevicesTab from '@/components/JailDevicesTab.vue'
import JailExecTab from '@/components/JailExecTab.vue'
import JailUpdatesTab from '@/components/JailUpdatesTab.vue'
import JailLogsTab from '@/components/JailLogsTab.vue'
import JailAccessTab from '@/components/JailAccessTab.vue'
import { isInstalledApp } from '@/lib/access'
import ProvisionPanel from '@/components/ProvisionPanel.vue'
import AddrCopy from '@jailops/shared/components/AddrCopy.vue'
import { shortAddr } from '@jailops/shared/lib/format'
import ErrorPanel from '@jailops/shared/components/ErrorPanel.vue'
import type { Jail, Lease, JailSnapshot } from '@jailops/shared/api/types'

const route = useRoute()
const name = computed(() => String(route.params.name))

const l = useLoad(async () => {
  const { data: jail, etag } = await api.jailConfig.get(name.value)
  // The server's expanded view says where every value comes from; without it only the jail's own values are shown.
  const expanded = await api.jailConfig.expanded(name.value).catch(() => undefined)
  const sources = expanded ? sourcesOf(expanded) : Object.fromEntries(Object.entries(flatten(jail.spec)).map(([k, v]) => [k, { value: v, source: 'jail' }]))
  const snaps = can('snapshots') ? await api.jails.snapshots(name.value).catch(() => [] as JailSnapshot[]) : []
  const nets = [...new Set(jail.spec.network?.map((n) => n.network) ?? [])]
  const leases = (await Promise.all(nets.map((n) => api.networks.leases(n).catch(() => [] as Lease[])))).flat().filter((x) => x.jail === jail.metadata.name)
  return { jail, etag, sources, snaps, leases }
})
const jail = computed<Jail | undefined>(() => l.data.value?.jail)
const snaps = computed(() => [...(l.data.value?.snaps ?? [])].sort((a, b) => (b.metadata.created_at ?? '').localeCompare(a.metadata.created_at ?? '')))
const maxWritten = computed(() => Math.max(1, ...snaps.value.map((x) => x.status.written ?? 0)))

type Tab = 'overview' | 'config' | 'devices' | 'updates' | 'logs' | 'access' | 'console'
const tabs = computed(() => ([
  ['overview', s.jail.overview, true],
  ['config', s.config.tab, true],
  ['devices', s.devices.tab, true],
  ['updates', s.updates.tab, hasExt('updates')],
  ['logs', s.logs.tab, hasExt('jail_logs')],
  // who may connect: for the jails an app installation made
  ['access', s.appAccess.tab, hasExt('app_access') && isInstalledApp(jail.value)],
  ['console', s.exec.tab, true],
] as Array<[Tab, string, boolean]>).filter((t) => t[2]))
// ?tab=logs&file=nginx/access.log opens a tab directly (the app pages link to a jail's log viewer).
const tab = ref<Tab>(tabs.value.some((t) => t[0] === route.query.tab) ? (route.query.tab as Tab) : 'overview')
// the jail arrives after the first render: a link to ?tab=access opens the tab once it is known to be an app
watch(jail, () => { if (route.query.tab === 'access' && tab.value === 'overview' && tabs.value.some((t) => t[0] === 'access')) tab.value = 'access' })
const logFile = typeof route.query.file === 'string' ? route.query.file : ''
const rollbackOpen = ref(false)
const rollbackSnap = ref<string | undefined>()
const upgradeOpen = ref(false)
const snapComment = ref('')
const actionError = ref<ApiError | null>(null)

function openRollback(snap?: string): void {
  rollbackSnap.value = snap
  rollbackOpen.value = true
}
async function act(a: Lifecycle): Promise<void> {
  actionError.value = null
  try {
    await lifecycle(name.value, a)
  } catch (e) {
    actionError.value = e instanceof ApiError ? e : null
  }
}
async function snapshot(): Promise<void> {
  actionError.value = null
  try {
    await runOp(`${s.jail.snapshotNow} ${name.value}`, () => api.jails.snapshot(name.value, { metadata: {}, spec: { jail: name.value, comment: snapComment.value || undefined } }))
    snapComment.value = ''
  } catch (e) {
    actionError.value = e instanceof ApiError ? e : null
  }
}

/** Rows that put the desired and the observed value of one field side by side. */
const rows = computed(() => {
  const j = jail.value
  if (!j) return []
  const specAddr = (j.spec.network ?? []).flatMap((n) => [n.ipv4, n.ipv6].filter((a): a is string => !!a && a !== 'none')).map((a) => a)
  const fixed = specAddr.filter((a) => a !== 'auto')
  const live = j.status.addresses ?? []
  const addrDiff = j.status.power === 'running' && fixed.some((a) => !live.some((x) => x.split('/')[0] === a.split('/')[0]))
  const pinned = j.spec.image.includes('#')
  const imgDiff = pinned && !!j.status.generation && !j.spec.image.endsWith(j.status.generation.replace(/^.*#/, '#')) && j.spec.image !== j.status.generation
  const wantRun = j.spec.boot?.autostart ? 'running (autostart)' : '-'
  return [
    { field: s.jail.image, spec: j.spec.image, status: j.status.generation || '-', diff: imgDiff },
    { field: s.jail.addresses, spec: specAddr.map(shortAddr).join(', ') || '-', status: live.map(shortAddr).join(', ') || s.jail.notRunning, diff: addrDiff },
    { field: s.jail.power, spec: wantRun, status: j.status.power, diff: !!j.spec.boot?.autostart && j.status.power !== 'running' },
  ]
})
const attentionItems = computed(() => {
  const j = jail.value
  if (!j) return []
  return (j.status.conditions ?? []).filter((c) => c.status === 'False')
})
</script>

<template>
  <div>
    <router-link class="back" to="/jails"><Icon name="chevronLeft" />{{ s.jail.back }}</router-link>
    <p v-if="l.error.value?.code === 'not_found'" class="empty">{{ s.jail.notFound }}</p>
    <LoadState :loading="l.loading.value" :error="l.error.value?.code === 'not_found' ? null : l.error.value" :has-data="!!jail" @retry="l.reload">
      <div v-if="jail" class="stack">
        <div class="page-head">
          <div class="row">
            <h1 class="mono">{{ jail.metadata.name }}</h1>
            <StatePill :state="jail.status.power" />
            <span v-if="jail.status.jid" class="faint mono">jid {{ jail.status.jid }}</span>
          </div>
          <div class="row">
            <button v-if="jail.status.power !== 'running'" class="btn" type="button" @click="act('start')"><Icon name="play" />{{ s.jails.start }}</button>
            <template v-else>
              <button class="btn" type="button" @click="act('stop')"><Icon name="square" />{{ s.jails.stop }}</button>
              <button class="btn" type="button" @click="act('restart')"><Icon name="rotateCw" />{{ s.jails.restart }}</button>
            </template>
            <button v-if="can('snapshots')" class="btn" type="button" @click="openRollback()" :disabled="!snaps.length"><Icon name="undo" />{{ s.jail.rollback }}</button>
            <button class="btn" type="button" @click="upgradeOpen = true"><Icon name="arrowUp" />{{ s.jail.upgrade }}</button>
          </div>
        </div>
        <ErrorPanel v-if="actionError" :error="actionError" />

        <Callout v-for="c in attentionItems" :key="c.type" tone="danger"><strong>{{ c.type }}</strong>{{ c.reason ? ` (${c.reason})` : '' }}: {{ c.message }}</Callout>

        <div class="tabs" role="tablist" :aria-label="s.jail.sections">
          <button v-for="[k, label] in tabs" :key="k" type="button" role="tab" :aria-selected="tab === k" :data-tab="k" @click="tab = k">{{ label }}</button>
        </div>
        <JailConfigTab v-if="tab === 'config'" :jail="jail" :sources="l.data.value?.sources ?? {}" @saved="l.reload" />
        <JailDevicesTab v-else-if="tab === 'devices'" :jail="jail" :sources="l.data.value?.sources ?? {}" @saved="l.reload" />
        <JailUpdatesTab v-else-if="tab === 'updates'" :jail="jail" @saved="l.reload" />
        <JailLogsTab v-else-if="tab === 'logs'" :jail="jail" :file="logFile" />
        <JailAccessTab v-else-if="tab === 'access'" :jail="jail" />
        <JailExecTab v-else-if="tab === 'console'" :jail="jail" />
        <template v-else>
        <section class="panel" aria-labelledby="svs">
          <header><h2 id="svs">{{ s.jail.specVsStatus }}</h2></header>
          <div class="table-wrap">
            <table data-test="spec-status">
              <thead><tr><th>&nbsp;</th><th>{{ s.jail.spec }}</th><th>{{ s.jail.status }}</th></tr></thead>
              <tbody>
                <tr v-for="r in rows" :key="r.field" :class="{ drift: r.diff }" :data-drift="r.diff">
                  <th scope="row">{{ r.field }}</th>
                  <td class="mono" :data-label="s.jail.spec">{{ r.spec }}</td>
                  <td class="mono" :data-label="s.jail.status">{{ r.status }}<span v-if="r.diff" class="pill warn" style="margin-left: 8px">{{ s.jails.drift }}</span></td>
                </tr>
                <tr><th scope="row">{{ s.jail.layout }}</th><td :data-label="s.jail.spec">{{ jail.spec.layout || 'thin' }}</td><td class="faint" :data-label="s.jail.status">-</td></tr>
                <tr><th scope="row">{{ s.jail.hostname }}</th><td class="mono" :data-label="s.jail.spec">{{ jail.spec.hostname || jail.metadata.name }}</td><td class="faint" :data-label="s.jail.status">-</td></tr>
                <tr><th scope="row">{{ s.jail.update }}</th><td :data-label="s.jail.spec">{{ jail.spec.update?.strategy || 'manual' }}<span v-if="jail.spec.update?.window" class="faint"> · {{ jail.spec.update.window }}</span></td><td class="faint" :data-label="s.jail.status">-</td></tr>
                <tr>
                  <th scope="row">{{ s.jail.resources }}</th>
                  <td class="mono" :data-label="s.jail.spec">{{ [jail.spec.resources?.cpu ? `cpu ${jail.spec.resources.cpu}` : '', jail.spec.resources?.memory ? `mem ${jail.spec.resources.memory}` : '', jail.spec.resources?.disk ? `disk ${jail.spec.resources.disk}` : '', jail.spec.resources?.pids ? `pids ${jail.spec.resources.pids}` : ''].filter(Boolean).join(' · ') || '-' }}</td>
                  <td class="faint" :data-label="s.jail.status">-</td>
                </tr>
                <tr><th scope="row">{{ s.jail.boot }}</th><td :data-label="s.jail.spec">{{ jail.spec.boot?.autostart ? s.jail.autostart : '-' }}<span v-if="jail.spec.encrypted" class="badge info" style="margin-left: 8px">{{ s.jail.encrypted }}</span></td><td class="faint" :data-label="s.jail.status">{{ jail.status.started_at && jail.status.power === 'running' ? ago(jail.status.started_at) : '-' }}</td></tr>
              </tbody>
            </table>
          </div>
          <div class="body">
            <h3>{{ s.jail.driftTitle }}</h3>
            <Callout v-if="jail.status.drift?.length" tone="warn" data-test="drift">
              <ul class="plain"><li v-for="d in jail.status.drift" :key="d" class="mono">{{ d }}</li></ul>
            </Callout>
            <p v-else class="muted">{{ s.jail.driftNone }}</p>
            <template v-if="jail.status.conditions?.length">
              <h3 style="margin-top: 12px">{{ s.jail.conditions }}</h3>
              <ul class="plain">
                <li v-for="c in jail.status.conditions" :key="c.type"><span class="pill" :class="c.status === 'True' ? 'ok' : c.status === 'False' ? 'danger' : ''">{{ c.type }}</span> <span class="muted">{{ c.message }}</span></li>
              </ul>
            </template>
          </div>
        </section>

        <section v-if="can('snapshots')" class="panel" aria-labelledby="snaps">
          <header>
            <h2 id="snaps">{{ s.jail.snapshots }}</h2>
            <form class="row" @submit.prevent="snapshot">
              <input v-model="snapComment" type="text" :placeholder="s.jail.snapshotComment" :aria-label="s.jail.snapshotComment" style="width: 200px" />
              <button class="btn btn-sm" type="submit"><Icon name="camera" />{{ s.jail.snapshotNow }}</button>
            </form>
          </header>
          <p v-if="!snaps.length" class="empty">{{ s.jail.snapshotsEmpty }}</p>
          <ol v-else class="timeline" data-test="timeline">
            <li v-for="x in snaps" :key="x.metadata.name">
              <div class="tl-main">
                <span class="mono">{{ x.metadata.name }}</span>
                <span class="faint">{{ stamp(x.metadata.created_at) }}</span>
                <span v-if="x.spec.comment" class="muted">{{ x.spec.comment }}</span>
              </div>
              <div class="tl-written" :title="s.jail.writtenHint">
                <span class="faint">{{ s.jail.written }}</span>
                <span class="mono num" data-test="written">{{ bytes(x.status.written ?? 0) }}</span>
                <div class="meter" aria-hidden="true"><span :style="{ width: ((x.status.written ?? 0) / maxWritten) * 100 + '%' }"></span></div>
              </div>
              <button class="btn btn-sm" type="button" @click="openRollback(x.metadata.name)"><Icon name="undo" />{{ s.jail.rollback }}</button>
            </li>
          </ol>
        </section>

        <div class="grid-2">
          <section class="panel">
            <header><h2>{{ s.jail.network }}</h2></header>
            <p v-if="!jail.spec.network?.length" class="empty">{{ s.jail.noNetwork }}</p>
            <div v-else class="table-wrap">
              <table>
                <thead><tr><th>{{ s.nav.networks }}</th><th>IPv4</th><th>IPv6</th><th>MAC</th></tr></thead>
                <tbody>
                  <tr v-for="n in jail.spec.network" :key="n.network">
                    <td><router-link class="mono" to="/networks">{{ n.network }}</router-link></td>
                    <td><AddrCopy :value="n.ipv4" /></td><td><AddrCopy :value="n.ipv6" /></td><td class="mono">{{ n.mac || '-' }}</td>
                  </tr>
                </tbody>
              </table>
            </div>
            <div class="body" v-if="l.data.value?.leases.length">
              <h3>{{ s.jail.leases }}</h3>
              <ul class="plain"><li v-for="x in l.data.value.leases" :key="x.address"><AddrCopy :value="x.address" /> <span class="faint">{{ x.network }}</span></li></ul>
            </div>
          </section>
          <section class="panel">
            <header><h2>{{ s.jail.firewall }}</h2></header>
            <div class="body">
              <p v-if="!jail.spec.firewall?.policy" class="muted">{{ s.jail.noPolicy }}</p>
              <dl v-else class="kv">
                <dt>{{ s.firewall.policy }}</dt><dd><router-link class="mono" to="/firewall">{{ jail.spec.firewall.policy }}</router-link></dd>
                <dt>{{ s.jail.tags }}</dt><dd class="mono">{{ jail.spec.firewall.tags?.join(', ') || '-' }}</dd>
              </dl>
            </div>
          </section>
        </div>

        <div class="grid-2">
          <section class="panel">
            <header><h2>{{ s.jail.volumes }}</h2></header>
            <p v-if="!jail.spec.volumes?.length" class="empty">{{ s.jail.noVolumes }}</p>
            <div v-else class="table-wrap">
              <table>
                <thead><tr><th>{{ s.jails.name }}</th><th>{{ s.jail.mount }}</th><th>{{ s.jail.quota }}</th></tr></thead>
                <tbody>
                  <tr v-for="v in jail.spec.volumes" :key="v.name">
                    <td class="mono">{{ v.name }}</td><td class="mono">{{ v.mount }}</td>
                    <td class="mono">{{ v.quota || '-' }}<span v-if="v.read_only" class="badge info" style="margin-left: 6px">{{ s.jail.readOnly }}</span></td>
                  </tr>
                </tbody>
              </table>
            </div>
          </section>
        </div>

        <ProvisionPanel v-if="hasExt('provisioning')" :jail="jail" @done="l.reload" />
        </template>
        <RollbackDialog :open="rollbackOpen" :jail="jail.metadata.name" :snapshots="snaps" :initial="rollbackSnap" @close="rollbackOpen = false" @done="l.reload" />
        <UpgradeDialog :open="upgradeOpen" :jail="jail" @close="upgradeOpen = false" @done="l.reload" />
      </div>
    </LoadState>
  </div>
</template>

<style>
.back { display: inline-flex; align-items: center; gap: 4px; margin-bottom: 8px; text-decoration: none; color: var(--text-2); }
.plain { list-style: none; margin: 0; padding: 0; display: grid; gap: 2px; }
tr.drift td, tr.drift th { background: var(--warn-bg); }
.timeline { list-style: none; margin: 0; padding: 0 12px; }
.timeline li { display: grid; grid-template-columns: minmax(0, 1fr) 180px auto; gap: 12px; align-items: center; padding: 8px 0 8px 16px; border-left: 2px solid var(--border-strong); position: relative; }
.timeline li::before { content: ''; position: absolute; left: -5px; top: 16px; width: 8px; height: 8px; border-radius: 50%; background: var(--surface); border: 2px solid var(--border-strong); }
.tl-main { display: flex; gap: 8px; flex-wrap: wrap; align-items: baseline; min-width: 0; }
.tl-written { display: grid; grid-template-columns: auto 1fr; gap: 0 8px; font-size: 12px; }
.tl-written .meter { grid-column: 1 / -1; height: 4px; margin-top: 2px; }
@media (max-width: 800px) { .timeline li { grid-template-columns: minmax(0, 1fr); gap: 4px; } }
</style>

<style>
/* spec-status-css: on a phone each row becomes a card, so no column is clipped or needs sideways scrolling */
@media (max-width: 600px) {
  [data-test='spec-status'] thead { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); }
  [data-test='spec-status'], [data-test='spec-status'] tbody, [data-test='spec-status'] tr, [data-test='spec-status'] th, [data-test='spec-status'] td { display: block; width: 100%; }
  [data-test='spec-status'] tr { padding: 8px 12px; border-bottom: 1px solid var(--border); }
  [data-test='spec-status'] th, [data-test='spec-status'] td { border: 0; padding: 2px 0; overflow-wrap: anywhere; background: none; }
  [data-test='spec-status'] td::before { content: attr(data-label) ': '; color: var(--text-2); font-family: var(--font, inherit); font-size: 12px; }
}
</style>
