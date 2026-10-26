<script setup lang="ts">
import { computed } from 'vue'
import { s } from '@/i18n'
import { projectState, currentProject, switchProject } from '@/composables/project'
import { quotaRows, tightest } from '@/lib/projectScope'
import { bytes, parseSize } from '@jailops/shared/lib/format'
import Meter from '@jailops/shared/components/Meter.vue'

const tight = computed(() => {
  const p = currentProject.value
  return p ? tightest(quotaRows(p.spec, p.status, parseSize)) : undefined
})
const fmt = (key: string, v: number) => (key === 'memory' ? bytes(v) : String(v))
</script>

<template>
  <div class="switcher" data-test="project-switcher">
    <label for="project-select" class="faint">{{ s.project.label }}</label>
    <select id="project-select" class="mono" :value="projectState.current" :aria-label="s.project.switch" data-test="project-select" @change="switchProject(($event.target as HTMLSelectElement).value)">
      <option v-if="!projectState.current" value="" disabled>{{ s.project.choose }}</option>
      <option v-for="p in projectState.projects" :key="p.metadata.name" :value="p.metadata.name">{{ p.metadata.name }}</option>
    </select>
    <span v-if="tight" class="qsum" data-test="switcher-quota">
      <span class="faint">{{ s.project.quotaNames[tight.key] }}</span>
      <span class="qmeter"><Meter :value="tight.pct" :label="s.project.quotaNames[tight.key]" :warn-at="75" :danger-at="95" /></span>
      <span class="mono">{{ fmt(tight.key, tight.used) }}/{{ fmt(tight.key, tight.limit) }}</span>
    </span>
    <router-link v-if="projectState.projects.length" class="faint" to="/projects">{{ s.nav.projects }}</router-link>
  </div>
</template>

<style>
.switcher { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.switcher select { width: auto; min-width: 140px; }
.qsum { display: inline-flex; align-items: center; gap: 6px; font-size: 12px; }
.qmeter { width: 80px; }
</style>
