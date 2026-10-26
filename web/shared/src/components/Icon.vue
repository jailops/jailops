<script setup lang="ts">
import { computed } from 'vue'
import { icons, type IconName } from '@jailops/shared/lib/icons'

const props = defineProps<{ name: IconName }>()
const style = computed(() => ({ '--icon': `url("${icons[props.name]}")` }))
</script>

<template>
  <span class="icon" :style="style" aria-hidden="true"></span>
</template>
