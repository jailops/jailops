<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { s } from '@/i18n'
import { api } from '@/api/endpoints'
import { useLoad } from '@jailops/shared/composables/load'
import { lifecycle, lifecycleMany, matches, type Lifecycle } from '@/composables/jailActions'
import { ago, shortAddr } from '@jailops/shared/lib/format'
import JailCreateForm from '@/components/JailCreateForm.vue'
import { currentProject } from '@/composables/project'
import StatePill from '@/components/StatePill.vue'
import Icon from '@jailops/shared/components/Icon.vue'
import LoadState from '@jailops/shared/components/LoadState.vue'
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
const filters: Array<PowerState | ''> = ['', 'running', 'stopped', 'starting', 'stopping', 'error']

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

async function bulk(a: Lifecycle): Promise<void> {
  await lifecycleMany(chosen.value, a)
  sel.value = new Set()
}
function one(n: string, a: Lifecycle): void {
  lifecycle(n, a).catch(() => undefined)
}
</script>

<template>
  <div>
    <div class="page-head"><h1>{{ s.jails.title }}</h1><button class="btn btn-primary" type="button" data-test="new-jail" @click="openCreate"><Icon name="plus" />{{ s.jails.create }}</button></div>
    <div class="row toolbar">
      <input v-model="q" class="grow" type="search" :placeholder="s.jails.filter" :aria-label="s.jails.filter" style="max-width: 420px" />
      <div class="row" role="group" aria-label="State">
        <button v-for="f in filters" :key="f" class="btn btn-sm" type="button" :aria-pressed="state === f" :class="{ 'btn-primary': state === f }" @click="state = f">{{ f ? (s.state as Record<string, string>)[f] : s.jails.all }}</button>
      </div>
    </div>
    <div v-if="chosen.length" class="row bulk panel" role="region" aria-label="Bulk actions">
      <strong>{{ s.jails.selected(chosen.length) }}</strong>
      <button class="btn btn-sm" type="button" @click="bulk('start')"><Icon name="play" />{{ s.jails.startSel }}</button>
      <button class="btn btn-sm" type="button" @click="bulk('stop')"><Icon name="square" />{{ s.jails.stopSel }}</button>
    </div>
    <LoadState :loading="l.loading.value" :error="l.error.value" :has-data="!!l.data.value" @retry="l.reload">
      <div class="panel">
        <p v-if="!shown.length" class="empty">{{ (l.data.value ?? []).length ? s.jails.empty : s.jails.emptyNone }}</p>
        <div v-else class="table-wrap">
          <table data-test="jails-table">
            <thead>
              <tr>
                <th style="width: 32px"><input type="checkbox" :checked="allSelected" :aria-label="s.jails.selectAll" @change="toggleAll" /></th>
                <th>{{ s.jails.name }}</th>
                <th>{{ s.jails.state }}</th>
                <th class="hide-sm">{{ s.jails.image }}</th>
                <th class="hide-sm">{{ s.jails.addresses }}</th>
                <th>{{ s.jails.drift }}</th>
                <th class="hide-sm">{{ s.jails.started }}</th>
                <th><span class="sr-only">Actions</span></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="j in shown" :key="j.metadata.name">
                <td><input type="checkbox" :checked="sel.has(j.metadata.name)" :aria-label="s.jails.selectRow(j.metadata.name)" @change="toggle(j.metadata.name)" /></td>
                <td><router-link class="mono" :to="`/jails/${j.metadata.name}`">{{ j.metadata.name }}</router-link></td>
                <td><StatePill :state="j.status.power" /></td>
                <td class="mono hide-sm">{{ j.spec.image }}<span v-if="j.status.generation" class="faint">{{ '\u00a0' + j.status.generation }}</span></td>
                <td class="mono hide-sm">{{ (j.status.addresses ?? []).map(shortAddr).join(', ') || '-' }}</td>
                <td><span v-if="j.status.drift?.length" class="pill warn">{{ s.overview.drift(j.status.drift.length) }}</span><span v-else class="faint">{{ s.jails.inSync }}</span></td>
                <td class="faint hide-sm nowrap">{{ j.status.power === 'running' ? ago(j.status.started_at) : '-' }}</td>
                <td class="nowrap">
                  <button v-if="j.status.power !== 'running'" class="btn btn-sm" type="button" @click="one(j.metadata.name, 'start')"><Icon name="play" />{{ s.jails.start }}</button>
                  <template v-else>
                    <button class="btn btn-sm" type="button" @click="one(j.metadata.name, 'stop')"><Icon name="square" />{{ s.jails.stop }}</button>
                    <button class="btn btn-sm btn-ghost btn-icon" type="button" :aria-label="s.jails.actionTitle(s.jails.restart, j.metadata.name)" @click="one(j.metadata.name, 'restart')"><Icon name="rotateCw" /></button>
                  </template>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </LoadState>
    <JailCreateForm :open="creating" :project="currentProject" :images="lookups.data.value?.images ?? []" :storage="lookups.data.value?.storage ?? []" :networks="lookups.data.value?.networks ?? []" @close="creating = false" @done="l.reload" />
  </div>
</template>

<style>
.toolbar { margin-bottom: 12px; }
.bulk { padding: 6px 12px; margin-bottom: 12px; background: var(--accent-soft); }
@media (max-width: 800px) { .hide-sm { display: none; } }
</style>
