<script setup lang="ts">
import { computed } from 'vue'

const props = defineProps<{ value: number; label: string; warnAt?: number; dangerAt?: number }>()
const pct = computed(() => Math.max(0, Math.min(100, props.value)))
const tone = computed(() => (pct.value >= (props.dangerAt ?? 90) ? 'danger' : pct.value >= (props.warnAt ?? 75) ? 'warn' : ''))
</script>

<template>
  <div class="meter" :class="tone" role="meter" :aria-label="label" aria-valuemin="0" aria-valuemax="100" :aria-valuenow="Math.round(pct)">
    <span :style="{ width: pct + '%' }"></span>
  </div>
</template>
