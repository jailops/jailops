<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { s } from '@/i18n'
import { hasExt } from '@jailops/shared/composables/session'
import { isGlobalAdmin } from '@/composables/project'
import UnifiedLogs from '@/components/UnifiedLogs.vue'
import LogForwardPanel from '@/components/LogForwardPanel.vue'

// Two sections: the merged lines of many jails, and (for a global admin) where the host forwards them.
type Tab = 'lines' | 'forwarding'
const route = useRoute()
const router = useRouter()
const canForward = computed(() => hasExt('log_forward') && isGlobalAdmin())
const tab = ref<Tab>(route.query.tab === 'forwarding' && canForward.value ? 'forwarding' : 'lines')
watch(tab, (t) => {
  const { tab: _t, ...rest } = route.query
  void router.replace({ query: t === 'forwarding' ? { ...rest, tab: 'forwarding' } : rest })
})
function onTabKey(e: KeyboardEvent): void {
  if (!canForward.value || (e.key !== 'ArrowRight' && e.key !== 'ArrowLeft')) return
  tab.value = tab.value === 'lines' ? 'forwarding' : 'lines'
  void Promise.resolve().then(() => (document.querySelector(`[data-tab="${tab.value}"]`) as HTMLElement | null)?.focus())
}
</script>

<template>
  <div>
    <div class="page-head">
      <div><h1>{{ s.ulogs.title }}</h1><p class="muted">{{ s.ulogs.lead }}</p></div>
    </div>
    <div v-if="canForward" class="tabs" role="tablist" :aria-label="s.ulogs.sections" @keydown="onTabKey">
      <button id="logs-tab-lines" type="button" role="tab" aria-controls="logs-panel" :aria-selected="tab === 'lines'" :tabindex="tab === 'lines' ? 0 : -1" data-tab="lines" @click="tab = 'lines'">{{ s.ulogs.viewTab }}</button>
      <button id="logs-tab-forwarding" type="button" role="tab" aria-controls="logs-panel" :aria-selected="tab === 'forwarding'" :tabindex="tab === 'forwarding' ? 0 : -1" data-tab="forwarding" @click="tab = 'forwarding'">{{ s.ulogs.forwardTab }}</button>
    </div>
    <div id="logs-panel" :role="canForward ? 'tabpanel' : undefined" :aria-labelledby="canForward ? `logs-tab-${tab}` : undefined">
      <LogForwardPanel v-if="tab === 'forwarding'" />
      <UnifiedLogs v-else />
    </div>
  </div>
</template>
