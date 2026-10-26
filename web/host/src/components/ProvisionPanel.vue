<script setup lang="ts">
import { computed, ref } from 'vue'
import { s } from '@/i18n'
import { api } from '@/api/endpoints'
import { runOp } from '@jailops/shared/composables/ops'
import { stamp } from '@jailops/shared/lib/format'
import ConfirmDialog from '@jailops/shared/components/ConfirmDialog.vue'
import Callout from '@jailops/shared/components/Callout.vue'
import type { Jail } from '@jailops/shared/api/types'

const props = defineProps<{ jail: Jail }>()
const emit = defineEmits<{ done: [] }>()
const d = computed(() => props.jail.spec.provision)
const doneAt = computed(() => props.jail.status.provisioned_at)
const complete = computed(() => !!doneAt.value && !doneAt.value.startsWith('0001'))
const again = ref(false)
const run = () => runOp(s.provision.againTitle(props.jail.metadata.name), () => api.provision.run(props.jail.metadata.name, { again: complete.value }))
const empty = computed(() => !d.value || (!d.value.packages?.length && !d.value.files?.length && !d.value.services?.enable?.length && !d.value.services?.start?.length && !d.value.commands?.length))
</script>

<template>
  <section class="panel" data-test="provision-panel">
    <header><h2>{{ s.provision.title }}</h2>
      <button class="btn btn-sm" type="button" :disabled="empty" data-test="provision-again" @click="again = true">{{ complete ? s.provision.again : s.provision.now }}</button></header>
    <div class="body stack">
      <p class="muted">{{ s.provision.lead }}</p>
      <div class="row"><span class="pill" :class="complete ? 'ok' : 'warn'" data-test="provision-state">{{ complete ? s.provision.complete : s.provision.incomplete }}</span>
        <span v-if="complete" class="faint">{{ s.provision.at(stamp(doneAt)) }}</span></div>
      <p v-if="empty" class="muted">{{ s.provision.nothing }}</p>
      <dl v-else class="kv">
        <template v-if="d?.packages?.length"><dt>{{ s.provision.packages }}</dt><dd class="mono">{{ d.packages.join(', ') }}</dd></template>
        <template v-if="d?.files?.length"><dt>{{ s.provision.files }}</dt><dd><div v-for="f in d.files" :key="f.destination" class="mono">{{ f.source }} &rarr; {{ f.destination }}<span v-if="f.owner || f.group || f.mode" class="faint"> ({{ [[f.owner, f.group].filter(Boolean).join(':'), f.mode].filter(Boolean).join(' ') }})</span></div></dd></template>
        <template v-if="d?.services?.enable?.length"><dt>{{ s.provision.enable }}</dt><dd class="mono">{{ d.services.enable.join(', ') }}</dd></template>
        <template v-if="d?.services?.start?.length"><dt>{{ s.provision.start }}</dt><dd class="mono">{{ d.services.start.join(', ') }}</dd></template>
        <template v-if="d?.commands?.length"><dt>{{ s.provision.commands }}</dt><dd><div v-for="c in d.commands" :key="c.join(' ')" class="mono">{{ c.join(' ') }}</div></dd></template>
      </dl>
    </div>
    <ConfirmDialog :open="again" :title="s.provision.againTitle(jail.metadata.name)" :confirm-label="complete ? s.provision.again : s.provision.now" :run="run" @close="again = false" @done="emit('done')"><Callout tone="warn">{{ s.provision.againText }}</Callout></ConfirmDialog>
  </section>
</template>
