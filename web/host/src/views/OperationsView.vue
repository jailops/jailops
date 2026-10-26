<script setup lang="ts">
import { computed } from 'vue'
import { s } from '@/i18n'
import { api } from '@/api/endpoints'
import { ApiError } from '@jailops/shared/api/client'
import { useLoad } from '@jailops/shared/composables/load'
import { ago } from '@jailops/shared/lib/format'
import { ref } from 'vue'
import LoadState from '@jailops/shared/components/LoadState.vue'
import ErrorPanel from '@jailops/shared/components/ErrorPanel.vue'
import type { Operation } from '@jailops/shared/api/types'

const l = useLoad(() => api.operations.list(), { every: 3000 })
const err = ref<ApiError | null>(null)
const cancelling = ref<Set<string>>(new Set())

const sorted = computed(() => [...(l.data.value ?? [])].sort((a, b) => b.created_at.localeCompare(a.created_at)))
const active = computed(() => sorted.value.filter((o) => o.status === 'pending' || o.status === 'running'))
const done = computed(() => sorted.value.filter((o) => o.status !== 'pending' && o.status !== 'running'))
const tone = (o: Operation) => ({ success: 'ok', failure: 'danger', running: 'info pulse', pending: 'info', cancelled: 'warn' })[o.status]

async function cancel(o: Operation): Promise<void> {
  err.value = null
  cancelling.value = new Set(cancelling.value).add(o.id)
  try {
    await api.operations.cancel(o.id)
    await l.reload()
  } catch (e) {
    err.value = e instanceof ApiError ? e : null
  } finally {
    const n = new Set(cancelling.value)
    n.delete(o.id)
    cancelling.value = n
  }
}
const res = (o: Operation) => Object.values(o.resources ?? {}).flat().join(', ')
</script>

<template>
  <div>
    <div class="page-head"><h1>{{ s.ops.title }}</h1></div>
    <ErrorPanel v-if="err" :error="err" style="margin-bottom: 12px" />
    <LoadState :loading="l.loading.value" :error="l.error.value" :has-data="!!l.data.value" @retry="l.reload">
      <p v-if="!sorted.length" class="empty panel">{{ s.ops.empty }}</p>
      <div class="stack">
        <template v-for="[title, list] in ([[s.ops.active, active], [s.ops.finished, done]] as const)" :key="title">
          <section v-if="list.length" class="panel">
            <header><h2>{{ title }} ({{ list.length }})</h2></header>
            <div class="table-wrap">
              <table>
                <thead><tr><th>{{ s.ops.description }}</th><th>{{ s.ops.status }}</th><th class="hide-sm">{{ s.ops.resources }}</th><th>{{ s.ops.progress }}</th><th>{{ s.ops.created }}</th><th></th></tr></thead>
                <tbody>
                  <tr v-for="o in list" :key="o.id" :data-op="o.id">
                    <td>{{ o.description }}<br /><span class="mono faint">{{ o.id }}</span>
                      <div v-if="o.error" class="err" style="color: var(--danger)"><span class="mono">{{ o.error.code }}</span>: {{ o.error.message }}<span v-if="o.error.hint" class="muted"> ({{ o.error.hint }})</span></div></td>
                    <td><span class="pill" :class="tone(o)">{{ o.status }}</span></td>
                    <td class="mono hide-sm">{{ res(o) }}</td>
                    <td style="min-width: 110px"><div v-if="o.status === 'running' || o.status === 'pending'" class="meter" role="progressbar" :aria-valuenow="o.progress ?? 0" aria-valuemin="0" aria-valuemax="100" :aria-label="o.description"><span :style="{ width: Math.max(4, o.progress ?? 0) + '%' }"></span></div><span v-else class="faint">-</span></td>
                    <td class="faint nowrap">{{ ago(o.created_at) }}</td>
                    <td><button v-if="o.may_cancel && (o.status === 'running' || o.status === 'pending')" class="btn btn-sm" type="button" :disabled="cancelling.has(o.id)" @click="cancel(o)">{{ cancelling.has(o.id) ? s.ops.cancelling : s.ops.cancel }}</button></td>
                  </tr>
                </tbody>
              </table>
            </div>
          </section>
        </template>
      </div>
    </LoadState>
  </div>
</template>
