<script setup lang="ts">
// A thin jail whose base package records could not be brought in step with its image at the last start has the
// BasePackages condition. This notice says why and repairs it on the spot (the pass every start makes).
import { computed, ref } from 'vue'
import { s } from '@/i18n'
import { api } from '@/api/endpoints'
import { ApiError } from '@jailops/shared/api/client'
import { runOp } from '@jailops/shared/composables/ops'
import { hasExt } from '@jailops/shared/composables/session'
import Callout from '@jailops/shared/components/Callout.vue'
import ErrorPanel from '@jailops/shared/components/ErrorPanel.vue'
import type { Jail } from '@jailops/shared/api/types'

const props = defineProps<{ jail: Jail }>()
const emit = defineEmits<{ done: [] }>()
const cond = computed(() => (props.jail.status.conditions ?? []).find((c) => c.type === 'BasePackages' && c.status === 'False'))
const busy = ref(false)
const error = ref<ApiError | null>(null)
async function repair(): Promise<void> {
  busy.value = true
  error.value = null
  try {
    await runOp(s.jail.repairPackagesTitle(props.jail.metadata.name), () => api.repairPackages.run(props.jail.metadata.name))
    emit('done')
  } catch (e) {
    error.value = e instanceof ApiError ? e : new ApiError({ message: String(e) })
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <Callout v-if="cond && hasExt('jail_repair_packages')" tone="warn" data-test="base-packages">
    <p>{{ s.jail.repairPackagesText }}</p>
    <p class="mono">{{ cond.message }}</p>
    <p v-if="jail.status.power === 'running'" class="muted" data-test="repair-packages-restart">{{ s.jail.repairPackagesRestart }}</p>
    <button v-else class="btn btn-sm" type="button" :disabled="busy" data-test="repair-packages" @click="repair">{{ busy ? s.jail.repairingPackages : s.jail.repairPackages }}</button>
    <ErrorPanel v-if="error" :error="error" />
  </Callout>
</template>
