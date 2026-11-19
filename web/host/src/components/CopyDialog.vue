<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { s } from '@/i18n'
import { api } from '@/api/endpoints'
import { ApiError } from '@jailops/shared/api/client'
import { runOp } from '@jailops/shared/composables/ops'
import { useFormCheck } from '@jailops/shared/composables/form'
import { nameHint } from '@jailops/shared/lib/names'
import { renameProblem, renameRule } from '@/lib/rename'
import type { Jail, JailSnapshot } from '@jailops/shared/api/types'
import Modal from '@jailops/shared/components/Modal.vue'
import ErrorPanel from '@jailops/shared/components/ErrorPanel.vue'

// Copy a jail (extension jail_copy): a new jail with the specification of this one and, by default, its data (a full,
// independent copy; the source may run). A snapshot of the source can be copied instead of its present state. The copy
// has addresses of its own, no volume and no exposure; Start starts it when it is made. Without the data the new jail
// is made from the specification alone and starts empty from its image.
const props = defineProps<{ open: boolean; jail: Jail; snapshots: JailSnapshot[] }>()
const emit = defineEmits<{ close: []; done: [name: string] }>()

const to = ref('')
const withData = ref(true)
const snapshot = ref('')
const start = ref(false)
const busy = ref(false)
const error = ref<ApiError | null>(null)
const check = useFormCheck(() => ({ 'cp-name': renameProblem('jail', props.jail.metadata.name, to.value, s.copy.same) }))
const running = computed(() => ['running', 'starting'].includes(props.jail.status.power))

watch(() => props.open, (o) => {
  if (!o) return
  to.value = ''
  withData.value = true
  snapshot.value = ''
  start.value = false
  error.value = null
  check.reset()
}, { immediate: true })

/** The specification of the copy when the data is left out: the source's, with a new address of each family it has. */
function specOnly(): Jail['spec'] {
  const spec = JSON.parse(JSON.stringify(props.jail.spec)) as Jail['spec']
  spec.volumes = undefined
  spec.expose = undefined
  for (const n of spec.network ?? []) {
    delete n.mac
    for (const k of ['ipv4', 'ipv6'] as const) if (n[k] && n[k] !== 'none') n[k] = 'auto'
  }
  return spec
}

const go = () => check.submit(async () => {
  if (busy.value) return
  const name = to.value
  busy.value = true
  error.value = null
  try {
    const labels = props.jail.metadata.labels
    if (withData.value) {
      await runOp(s.copy.op(props.jail.metadata.name, name), () => api.jails.copy(props.jail.metadata.name, { name, ...(snapshot.value ? { snapshot: snapshot.value } : {}) }))
    } else {
      await runOp(s.copy.op(props.jail.metadata.name, name), () => api.jails.create({ metadata: { name, ...(labels ? { labels } : {}) } as Jail['metadata'], spec: specOnly() }))
    }
    if (start.value) await runOp(s.copy.starting(name), () => api.jails.action(name, { action: 'start' }))
    emit('done', name)
    emit('close')
  } catch (e) {
    error.value = e instanceof ApiError ? e : new ApiError({ message: String(e) })
  } finally {
    busy.value = false
  }
})
</script>

<template>
  <Modal :open="open" :title="s.copy.title(jail.metadata.name)" @close="emit('close')">
    <form id="cp" novalidate class="stack" data-test="copy-form" @submit.prevent="go">
      <p class="muted" style="margin: 0">{{ s.copy.lead }}</p>
      <div class="field"><label for="cp-name">{{ s.copy.newName }} <span class="req" aria-hidden="true">*</span></label>
        <input id="cp-name" v-model="to" class="mono" type="text" autocomplete="off" spellcheck="false" :placeholder="jail.metadata.name + '-2'" required :aria-invalid="!!check.err('cp-name')" aria-describedby="cp-name-h cp-name-e" data-test="copy-name" @input="check.touch('cp-name')" />
        <span id="cp-name-h" class="hint">{{ nameHint(renameRule.jail) }}</span>
        <span id="cp-name-e" class="field-error" role="alert">{{ check.err('cp-name') }}</span></div>
      <div class="field"><label class="check"><input v-model="withData" type="checkbox" data-test="copy-data" aria-describedby="cp-data-h" /> {{ s.copy.withData }}</label>
        <span id="cp-data-h" class="hint">{{ withData ? (running ? s.copy.dataHintRunning : s.copy.dataHint) : s.copy.specOnlyHint }}</span></div>
      <div v-if="withData" class="field"><label for="cp-snap">{{ s.copy.snapshot }}</label>
        <select id="cp-snap" v-model="snapshot" data-test="copy-snapshot">
          <option value="">{{ s.copy.now }}</option>
          <option v-for="sn in snapshots" :key="sn.metadata.name" :value="sn.metadata.name">{{ sn.metadata.name }}</option>
        </select></div>
      <div class="field"><label class="check"><input v-model="start" type="checkbox" data-test="copy-start" /> {{ s.copy.start }}</label></div>
      <ErrorPanel v-if="error" :error="error" />
    </form>
    <template #footer>
      <button class="btn" type="button" @click="emit('close')">{{ s.app.cancel }}</button>
      <button class="btn btn-primary" type="submit" form="cp" :disabled="busy" data-test="copy-go">{{ busy ? s.copy.working : s.copy.go }}</button>
    </template>
  </Modal>
</template>
