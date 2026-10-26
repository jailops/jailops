<script setup lang="ts">
import { computed } from 'vue'
import { s } from '@/i18n'
import { session } from '@jailops/shared/composables/session'
import { allows, who } from '@/lib/roles'
import Callout from '@jailops/shared/components/Callout.vue'
import type { NodeRole } from '@/types'

const props = defineProps<{ need: NodeRole; /** the page needs it to show anything */ gate?: boolean }>()
const ok = computed(() => allows(session.role, props.need))
</script>

<template>
  <Callout v-if="!ok" tone="warn" data-test="role-note">{{ s.roles.lacking(who(need), session.role) }}</Callout>
  <p v-else-if="!gate" class="faint who" data-test="who-may">{{ s.roles.needs(who(need)) }}</p>
</template>

<style>
.who { margin: 0; font-size: 12px; }
</style>
