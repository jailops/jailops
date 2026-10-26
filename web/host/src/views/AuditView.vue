<script setup lang="ts">
import { computed, ref } from 'vue'
import { s } from '@/i18n'
import { api } from '@/api/endpoints'
import { useLoad } from '@jailops/shared/composables/load'
import { stamp } from '@jailops/shared/lib/format'
import LoadState from '@jailops/shared/components/LoadState.vue'

const l = useLoad(() => api.audit.list())
const q = ref('')
const rows = computed(() => {
  const n = q.value.trim().toLowerCase()
  return (l.data.value ?? []).filter((r) => !n || [r.requestor, r.action, r.resource ?? ''].some((x) => x.toLowerCase().includes(n)))
})
</script>

<template>
  <div>
    <div class="page-head"><h1>{{ s.audit.title }}</h1></div>
    <div class="row toolbar"><input v-model="q" type="search" :placeholder="s.audit.filter" :aria-label="s.audit.filter" style="max-width: 420px" /></div>
    <LoadState :loading="l.loading.value" :error="l.error.value" :has-data="!!l.data.value" @retry="l.reload">
      <div class="panel">
        <p v-if="!rows.length" class="empty">{{ s.audit.empty }}</p>
        <div v-else class="table-wrap">
          <table>
            <thead><tr><th>{{ s.audit.time }}</th><th>{{ s.audit.who }}</th><th>{{ s.audit.action }}</th><th>{{ s.audit.resource }}</th><th>{{ s.audit.result }}</th><th class="hide-sm">{{ s.audit.source }}</th></tr></thead>
            <tbody>
              <tr v-for="r in rows" :key="r.id">
                <td class="mono faint nowrap">{{ stamp(r.timestamp) }}</td>
                <td class="mono">{{ r.requestor }}</td>
                <td class="mono">{{ r.action }}</td>
                <td class="mono">{{ r.resource || '-' }}</td>
                <td><span v-if="r.result" class="pill" :class="r.result === 'ok' ? 'ok' : 'danger'">{{ r.result }}</span></td>
                <td class="mono faint hide-sm">{{ r.source_ip || '-' }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </LoadState>
  </div>
</template>
