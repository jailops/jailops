<script setup lang="ts">
import { computed, ref } from 'vue'
import { s } from '@/i18n'
import { api } from '@/api/endpoints'
import { useLoad } from '@jailops/shared/composables/load'
import { bytes, percent } from '@jailops/shared/lib/format'
import Meter from '@jailops/shared/components/Meter.vue'
import Icon from '@jailops/shared/components/Icon.vue'
import Callout from '@jailops/shared/components/Callout.vue'
import type { Top } from '@jailops/shared/api/types'
import { sortJails, topSorts, cpuText, uptimeText, share, netTotal, type TopSort } from '@/lib/top'

const project = ref('')
const sort = ref<TopSort>('cpu')
const paused = ref(false)
const known = ref<string[]>([])

// The page asks again every two seconds; a pause keeps the last frame on screen.
let last: Top | undefined
const l = useLoad(async (): Promise<Top> => {
  if (paused.value && last) return last
  const t = await api.top(project.value || undefined)
  last = t
  if (!project.value) known.value = [...new Set(t.jails.map((j) => j.project).filter(Boolean))].sort()
  return t
}, { every: 2000 })

const frame = computed(() => l.data.value)
const jails = computed(() => sortJails(frame.value?.jails ?? [], sort.value))
const host = computed(() => frame.value?.host)
const memPct = computed(() => (host.value?.memory_total && host.value.memory_used !== undefined ? percent(host.value.memory_used, host.value.memory_total) : undefined))
const poolPct = computed(() => (host.value?.pool_size && host.value.pool_allocated !== undefined ? percent(host.value.pool_allocated, host.value.pool_size) : undefined))
const rate = (n: number | undefined): string => (n === undefined ? 'n/a' : `${bytes(n)}/s`)
function pick(p: string): void {
  project.value = p
  void l.reload()
}
</script>

<template>
  <div>
    <div class="page-head">
      <h1>{{ s.top.title }}</h1>
      <span class="pill" :class="paused ? 'warn' : 'ok pulse'" data-test="top-link">{{ paused ? s.top.pausedPill : s.app.live }}</span>
    </div>
    <div class="row toolbar">
      <select :value="project" :aria-label="s.top.project" style="width: auto" data-test="top-project" @change="pick(($event.target as HTMLSelectElement).value)">
        <option value="">{{ s.top.allProjects }}</option>
        <option v-for="p in known" :key="p" :value="p">{{ p }}</option>
      </select>
      <select v-model="sort" :aria-label="s.top.sortBy" style="width: auto" data-test="top-sort">
        <option v-for="k in topSorts" :key="k" :value="k">{{ s.top.sorts[k] }}</option>
      </select>
      <button class="btn" type="button" :aria-pressed="paused" @click="paused = !paused"><Icon :name="paused ? 'play' : 'pause'" />{{ paused ? s.top.resume : s.top.pause }}</button>
    </div>

    <Callout v-if="l.error.value" tone="danger">{{ l.error.value.message }}</Callout>
    <Callout v-for="n in frame?.notes ?? []" :key="n" tone="warn" data-test="top-note">{{ n }}</Callout>

    <div v-if="host" class="grid-2" data-test="top-host">
      <section class="panel"><div class="body">
        <p class="muted">{{ s.top.load }}</p>
        <p class="mono">{{ host.load_1.toFixed(2) }} {{ host.load_5.toFixed(2) }} {{ host.load_15.toFixed(2) }}</p>
        <p class="muted">{{ s.top.cpu }}: <span class="mono">{{ host.cpu_percent === undefined ? 'n/a' : Math.round(host.cpu_percent) + '%' }}</span> · {{ host.cpus }} {{ s.top.cpus }}</p>
        <Meter v-if="host.cpu_percent !== undefined" :value="host.cpu_percent" :label="s.top.cpu" />
      </div></section>
      <section class="panel"><div class="body">
        <p class="muted">{{ s.top.memory }}</p>
        <p class="mono">{{ host.memory_used === undefined ? 'n/a' : bytes(host.memory_used) }} / {{ bytes(host.memory_total) }}</p>
        <Meter v-if="memPct !== undefined" :value="memPct" :label="s.top.memory" />
        <p class="muted">{{ s.top.arc }}: <span class="mono">{{ host.arc_bytes === undefined ? 'n/a' : bytes(host.arc_bytes) }}</span></p>
      </div></section>
      <section class="panel"><div class="body">
        <p class="muted">{{ s.top.pool }} <span class="mono">{{ host.pool }}</span></p>
        <p class="mono">{{ host.pool_allocated === undefined ? 'n/a' : bytes(host.pool_allocated) }} {{ s.top.used }} · {{ host.pool_free === undefined ? 'n/a' : bytes(host.pool_free) }} {{ s.top.free }}</p>
        <Meter v-if="poolPct !== undefined" :value="poolPct" :label="s.top.pool" />
      </div></section>
      <section class="panel"><div class="body">
        <p class="muted">{{ s.top.running }}</p>
        <p class="mono" data-test="top-running">{{ host.running_jails }}</p>
      </div></section>
    </div>

    <div class="panel">
      <p v-if="!l.loading.value && !jails.length" class="empty">{{ s.top.empty }}</p>
      <div v-else class="table-wrap">
        <table data-test="top-table" class="rtable">
          <thead><tr>
            <th>{{ s.top.jail }}</th><th>{{ s.top.cpu }}</th><th>{{ s.top.memory }}</th><th>{{ s.top.processes }}</th><th>{{ s.top.disk }}</th><th>{{ s.top.netIn }}</th><th>{{ s.top.netOut }}</th><th>{{ s.top.uptime }}</th>
          </tr></thead>
          <tbody>
            <tr v-for="j in jails" :key="j.project + '/' + j.name" :data-jail="j.name" :data-project="j.project">
              <td :data-label="s.top.jail"><router-link class="mono" :to="`/jails/${j.name}`">{{ j.name }}</router-link> <span v-if="j.project" class="faint">{{ j.project }}</span></td>
              <td :data-label="s.top.cpu"><span class="mono">{{ cpuText(j) }}</span><span v-if="j.cpu_limit_percent" class="faint"> / {{ j.cpu_limit_percent }}%</span>
                <Meter v-if="share(j.cpu_percent, j.cpu_limit_percent) !== undefined" :value="share(j.cpu_percent, j.cpu_limit_percent)!" :label="s.top.cpu" /></td>
              <td :data-label="s.top.memory"><span class="mono">{{ j.memory_bytes === undefined ? 'n/a' : (j.approximate ? '~' : '') + bytes(j.memory_bytes) }}</span><span v-if="j.memory_limit_bytes" class="faint"> / {{ bytes(j.memory_limit_bytes) }}</span></td>
              <td :data-label="s.top.processes" class="mono">{{ j.processes ?? 'n/a' }}</td>
              <td :data-label="s.top.disk"><span class="mono">{{ j.disk_bytes === undefined ? 'n/a' : bytes(j.disk_bytes) }}</span><span v-if="j.disk_quota_bytes" class="faint"> / {{ bytes(j.disk_quota_bytes) }}</span></td>
              <td :data-label="s.top.netIn" class="mono">{{ rate(j.net_in_bytes_per_second) }}</td>
              <td :data-label="s.top.netOut" class="mono">{{ rate(j.net_out_bytes_per_second) }}</td>
              <td :data-label="s.top.uptime" class="mono">{{ uptimeText(j.uptime_seconds) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
    <p v-if="netTotal(jails[0] ?? { project: '', name: '' }) === undefined && jails.length" class="muted">{{ s.top.noRates }}</p>
  </div>
</template>
