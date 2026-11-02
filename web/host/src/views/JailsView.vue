<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { s } from '@/i18n'
import { api } from '@/api/endpoints'
import { useLoad } from '@jailops/shared/composables/load'
import { can } from '@jailops/shared/composables/session'
import { askLifecycle, askStopMany, lifecycleMany, matches, isLocked, type Lifecycle } from '@/composables/jailActions'
import UnlockDialog from '@/components/UnlockDialog.vue'
import LockChip from '@/components/LockChip.vue'
import { shortAddr } from '@jailops/shared/lib/format'
import JailCreateForm from '@/components/JailCreateForm.vue'
import { currentProject } from '@/composables/project'
import StatePill from '@/components/StatePill.vue'
import Icon from '@jailops/shared/components/Icon.vue'
import LoadState from '@jailops/shared/components/LoadState.vue'
import TimeStamp from '@jailops/shared/components/TimeStamp.vue'
import type { PowerState } from '@jailops/shared/api/types'

const route = useRoute()
const router = useRouter()
const l = useLoad(() => api.jails.list())
const creating = ref(false)
const lookups = useLoad(async () => ({
  images: await api.images.list().catch(() => []), storage: await api.storage.list().catch(() => []), networks: await api.networks.list().catch(() => []),
}))
function openCreate(): void {
  void lookups.reload()
  creating.value = true
}
const q = ref(typeof route.query.q === 'string' ? route.query.q : '')
const state = ref<PowerState | ''>((typeof route.query.state === 'string' ? route.query.state : '') as PowerState | '')
const sel = ref<Set<string>>(new Set())
const filters: Array<PowerState | ''> = ['', 'running', 'stopped', 'starting', 'stopping', 'error', 'locked']
const unlocking = ref('')

watch([q, state], () => void router.replace({ query: { ...(q.value ? { q: q.value } : {}), ...(state.value ? { state: state.value } : {}) } }))

const shown = computed(() =>
  (l.data.value ?? []).filter((j) => (!state.value || j.status.power === state.value) && matches(j, q.value)).sort((a, b) => a.metadata.name.localeCompare(b.metadata.name)),
)
const allSelected = computed(() => shown.value.length > 0 && shown.value.every((j) => sel.value.has(j.metadata.name)))
function toggleAll(): void {
  sel.value = allSelected.value ? new Set() : new Set(shown.value.map((j) => j.metadata.name))
}
function toggle(n: string): void {
  const x = new Set(sel.value)
  if (x.has(n)) x.delete(n)
  else x.add(n)
  sel.value = x
}
const chosen = computed(() => [...sel.value].filter((n) => shown.value.some((j) => j.metadata.name === n)))

async function bulkStart(): Promise<void> {
  await lifecycleMany(chosen.value, 'start')
  sel.value = new Set()
}
function bulkStop(): void {
  askStopMany(shown.value.filter((j) => sel.value.has(j.metadata.name)), () => (sel.value = new Set()))
}
function one(n: string, a: Lifecycle, power: string): void {
  askLifecycle(n, a, power)
}
</script>

<template>
  <div>
    <div class="page-head"><h1>{{ s.jails.title }}</h1><button class="btn btn-primary" type="button" data-test="new-jail" @click="openCreate"><Icon name="plus" />{{ s.jails.create }}</button></div>
    <div class="row toolbar">
      <input v-model="q" class="grow" type="search" :placeholder="s.jails.filter" :aria-label="s.jails.filter" style="max-width: 420px" />
      <div class="row" role="group" :aria-label="s.jails.state">
        <button v-for="f in filters" :key="f" class="btn btn-sm" type="button" :aria-pressed="state === f" :class="{ 'btn-primary': state === f }" @click="state = f">{{ f ? (s.state as Record<string, string>)[f] : s.jails.all }}</button>
      </div>
    </div>
    <div v-if="chosen.length" class="row bulk panel" role="region" :aria-label="s.jails.actions">
      <strong>{{ s.jails.selected(chosen.length) }}</strong>
      <div class="row-actions">
        <button class="btn btn-sm" type="button" @click="bulkStart"><Icon name="play" />{{ s.jails.startSel }}</button>
        <button class="btn btn-sm" type="button" data-test="bulk-stop" @click="bulkStop"><Icon name="square" />{{ s.jails.stopSel }}</button>
      </div>
    </div>
    <LoadState :loading="l.loading.value" :error="l.error.value" :has-data="!!l.data.value" @retry="l.reload">
      <div class="panel">
        <p v-if="!shown.length && (l.data.value ?? []).length" class="empty">{{ s.jails.empty }}</p>
        <div v-else-if="!shown.length" class="empty jails-empty" data-test="jails-empty">
          <p>{{ s.jails.emptyNone }}</p>
          <div class="row-actions" style="justify-content: center">
            <button class="btn btn-primary" type="button" data-test="empty-new-jail" @click="openCreate"><Icon name="plus" />{{ s.jails.create }}</button>
            <router-link v-if="can('apps')" class="btn" to="/apps"><Icon name="store" />{{ s.jails.emptyApps }}</router-link>
          </div>
        </div>
        <div v-else class="table-wrap">
          <table class="rtable" data-test="jails-table">
            <thead>
              <tr>
                <th style="width: 32px"><input type="checkbox" :checked="allSelected" :aria-label="s.jails.selectAll" @change="toggleAll" /></th>
                <th>{{ s.jails.name }}</th>
                <th>{{ s.jails.state }}</th>
                <th class="hide-sm">{{ s.jails.image }}</th>
                <th class="hide-sm">{{ s.jails.addresses }}</th>
                <th>{{ s.jails.drift }}</th>
                <th class="hide-sm">{{ s.jails.started }}</th>
                <th><span class="sr-only">{{ s.jails.actions }}</span></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="j in shown" :key="j.metadata.name" :data-jail="j.metadata.name">
                <td class="check-cell"><input type="checkbox" :checked="sel.has(j.metadata.name)" :aria-label="s.jails.selectRow(j.metadata.name)" @change="toggle(j.metadata.name)" /></td>
                <td :data-label="s.jails.name"><router-link class="mono jail-link" :to="`/jails/${j.metadata.name}`">{{ j.metadata.name }}</router-link></td>
                <td :data-label="s.jails.state"><StatePill :state="j.status.power" /><LockChip v-if="j.status.locked?.length" :volumes="j.status.locked" :jail="j.metadata.name" /></td>
                <td class="mono hide-sm" :data-label="s.jails.image">{{ j.spec.image }}<span v-if="j.status.generation" class="faint">{{ ' ' + j.status.generation }}</span></td>
                <td class="mono hide-sm" :data-label="s.jails.addresses">{{ (j.status.addresses ?? []).map(shortAddr).join(', ') || '-' }}</td>
                <td :data-label="s.jails.drift"><span v-if="j.status.drift?.length" class="pill warn">{{ s.overview.drift(j.status.drift.length) }}</span><span v-else class="faint">{{ s.jails.inSync }}</span></td>
                <td class="faint hide-sm nowrap started-cell" :data-label="s.jails.started"><TimeStamp v-if="j.status.power === 'running'" :ts="j.status.started_at" /><span v-else>-</span></td>
                <td class="actions">
                  <div class="row-actions">
                    <button v-if="isLocked(j)" class="btn btn-sm" type="button" :data-unlock="j.metadata.name" :aria-label="s.jails.actionTitle(s.keys.unlock, j.metadata.name)" @click="unlocking = j.metadata.name"><Icon name="lockOpen" />{{ s.keys.unlock }}</button>
                    <button v-else-if="j.status.power !== 'running'" class="btn btn-sm" type="button" :aria-label="s.jails.actionTitle(s.jails.start, j.metadata.name)" @click="one(j.metadata.name, 'start', j.status.power)"><Icon name="play" />{{ s.jails.start }}</button>
                    <template v-else>
                      <button class="btn btn-sm" type="button" :aria-label="s.jails.actionTitle(s.jails.stop, j.metadata.name)" :data-stop="j.metadata.name" @click="one(j.metadata.name, 'stop', j.status.power)"><Icon name="square" />{{ s.jails.stop }}</button>
                      <button class="btn btn-sm" type="button" :aria-label="s.jails.actionTitle(s.jails.restart, j.metadata.name)" :title="s.jails.restart" @click="one(j.metadata.name, 'restart', j.status.power)"><Icon name="rotateCw" /><span class="restart-word">{{ s.jails.restart }}</span></button>
                    </template>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </LoadState>
    <UnlockDialog :open="!!unlocking" :jail="unlocking" @close="unlocking = ''" @done="l.reload" />
    <JailCreateForm :open="creating" :project="currentProject" :images="lookups.data.value?.images ?? []" :storage="lookups.data.value?.storage ?? []" :networks="lookups.data.value?.networks ?? []" @close="creating = false" @done="l.reload" />
  </div>
</template>

<style>
.toolbar { margin-bottom: 12px; }
.bulk { padding: 6px 12px; margin-bottom: 12px; background: var(--accent-soft); }
.jails-empty p { margin-bottom: 12px; }
.jail-link { font-weight: 500; }
@media (max-width: 800px) { .hide-sm { display: none; } }
/* phone cards: the label beside the value keeps each card short; the start time stays on the jail page */
@media (max-width: 600px) {
  table.rtable[data-test='jails-table'] td[data-label]:not([data-label='']) { position: relative; padding-left: 92px; min-height: 24px; }
  table.rtable[data-test='jails-table'] td[data-label]:not([data-label=''])::before { position: absolute; left: 0; top: 4px; width: 88px; }
  table.rtable[data-test='jails-table'] td.started-cell { display: none; }
}
@media (min-width: 601px) { [data-test='jails-table'] td.actions .row-actions { flex-wrap: nowrap; justify-content: flex-end; } }
</style>
