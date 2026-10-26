<script setup lang="ts">
import { computed, ref } from 'vue'
import { s } from '@/i18n'
import { eventState } from '@jailops/shared/composables/events'
import { stamp } from '@jailops/shared/lib/format'
import Icon from '@jailops/shared/components/Icon.vue'
import type { ApiEvent } from '@jailops/shared/api/types'

const type = ref('')
const paused = ref(false)
const held = ref<ApiEvent[] | null>(null)

const view = computed(() => (held.value ?? eventState.items).filter((e) => !type.value || e.type === type.value))
function pause(): void {
  paused.value = !paused.value
  held.value = paused.value ? [...eventState.items] : null
}
function clear(): void {
  eventState.items.length = 0
  if (held.value) held.value = []
}
function summary(e: ApiEvent): { text: string; source: string; who: string } {
  const m = (e.metadata ?? {}) as Record<string, unknown>
  if (e.type === 'lifecycle') return { text: String(m.action ?? ''), source: String(m.source ?? ''), who: String(m.requestor ?? '') }
  if (e.type === 'operation') return { text: `${m.description ?? ''} ${m.status ?? ''}`.trim(), source: String(m.id ?? ''), who: '' }
  return { text: String(m.message ?? JSON.stringify(m)), source: '', who: '' }
}
</script>

<template>
  <div>
    <div class="page-head">
      <h1>{{ s.ops.eventsTitle }}</h1>
      <span class="pill" :class="eventState.link === 'live' ? 'ok pulse' : 'warn'" data-test="link">{{ eventState.link === 'live' ? s.app.live : eventState.link === 'reconnecting' ? s.app.reconnecting(Math.ceil(eventState.retryIn / 1000)) : s.app.connecting }}</span>
    </div>
    <div class="row toolbar">
      <select v-model="type" :aria-label="s.ops.filterType" style="width: auto"><option value="">{{ s.ops.allTypes }}</option><option>lifecycle</option><option>operation</option><option>logging</option></select>
      <button class="btn" type="button" :aria-pressed="paused" @click="pause"><Icon name="pause" />{{ paused ? s.ops.resume : s.ops.pause }}</button>
      <button class="btn" type="button" @click="clear">{{ s.ops.clear }}</button>
    </div>
    <p v-if="paused" class="muted">{{ s.ops.paused }}</p>
    <div class="panel">
      <p v-if="!view.length" class="empty">{{ s.ops.eventsEmpty }}</p>
      <div v-else class="table-wrap">
        <table data-test="events">
          <thead><tr><th>{{ s.ops.time }}</th><th>{{ s.ops.type }}</th><th>Event</th><th class="hide-sm">{{ s.ops.source }}</th><th class="hide-sm">{{ s.ops.requestor }}</th></tr></thead>
          <tbody>
            <tr v-for="(e, i) in view" :key="e.timestamp + i">
              <td class="mono faint nowrap">{{ stamp(e.timestamp) }}</td>
              <td><span class="pill" :class="e.type === 'lifecycle' ? 'info' : e.type === 'operation' ? '' : 'warn'">{{ e.type }}</span></td>
              <td class="mono">{{ summary(e).text }}</td>
              <td class="mono faint hide-sm">{{ summary(e).source }}</td>
              <td class="mono faint hide-sm">{{ summary(e).who }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </div>
</template>
