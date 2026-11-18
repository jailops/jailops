<script setup lang="ts">
import { s } from '@/i18n'
import { api } from '@/api/endpoints'
import { useLoad } from '@jailops/shared/composables/load'
import LoadState from '@jailops/shared/components/LoadState.vue'
import Callout from '@jailops/shared/components/Callout.vue'
import type { ExposedJail } from '@jailops/shared/api/types'

// Everything the Internet reaches, across the jails of the project (GET /exposed): the review of what is open to the world.
const l = useLoad(() => api.reach.exposed())
const to = (e: ExposedJail): string => (e.via === 'forward' ? `${e.host_address || s.reach.host}:${e.host_port} -> ${e.address}` : e.address)
const from = (e: ExposedJail): string => (e.restricted ? e.from.join(', ') : s.reach.any)
</script>

<template>
  <section class="panel" aria-labelledby="exp-h" data-test="exposed-panel">
    <header><h2 id="exp-h">{{ s.reach.exposedTitle }}</h2></header>
    <div class="body stack">
      <p class="muted">{{ s.reach.exposedLead }}</p>
      <LoadState :loading="l.loading.value" :error="l.error.value" :has-data="!!l.data.value" @retry="l.reload">
        <p v-if="!l.data.value?.entries.length" class="empty" data-test="exposed-empty">{{ s.reach.exposedNone }}</p>
        <div v-else class="table-wrap">
          <table class="rtable" data-test="exposed-rows">
            <thead><tr><th>{{ s.reach.jail }}</th><th>{{ s.reach.proto }}</th><th>{{ s.reach.ports }}</th><th>{{ s.reach.family }}</th><th>{{ s.reach.address }}</th><th>{{ s.reach.limited }}</th><th>{{ s.reach.by }}</th></tr></thead>
            <tbody>
              <tr v-for="(e, i) in l.data.value.entries" :key="i">
                <td :data-label="s.reach.jail" class="mono">{{ e.jail }}</td>
                <td :data-label="s.reach.proto" class="mono">{{ e.proto }}</td>
                <td :data-label="s.reach.ports" class="mono">{{ e.via === 'forward' ? e.host_port : e.ports || '-' }}</td>
                <td :data-label="s.reach.family">IPv{{ e.family }}</td>
                <td :data-label="s.reach.address" class="mono">{{ to(e) }}</td>
                <td :data-label="s.reach.limited" class="mono">{{ from(e) }}</td>
                <td :data-label="s.reach.by" class="muted">{{ e.by }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <Callout v-for="m in l.data.value?.errors ?? []" :key="m" tone="info">{{ m }}</Callout>
      </LoadState>
    </div>
  </section>
</template>
