<script setup lang="ts">
import { computed, ref } from 'vue'
import { s } from '@/i18n'
import { api } from '@/api/endpoints'
import { useLoad } from '@jailops/shared/composables/load'
import { runOp } from '@jailops/shared/composables/ops'
import { ApiError } from '@jailops/shared/api/client'
import Callout from '@jailops/shared/components/Callout.vue'
import CopyField from '@jailops/shared/components/CopyField.vue'
import Modal from '@jailops/shared/components/Modal.vue'
import ErrorPanel from '@jailops/shared/components/ErrorPanel.vue'
import LoadState from '@jailops/shared/components/LoadState.vue'
import { isGlobalAdmin } from '@/composables/project'

const l = useLoad(() => api.backups.keyStatus())
const admin = computed(() => isGlobalAdmin())
const recipients = ref('')
const list = () => recipients.value.split(/[\s,]+/).filter(Boolean)
const busy = ref(false)
const error = ref<ApiError | null>(null)
// The recovery key lives only in this ref, only while the dialog is open.
const created = ref<{ domain: string; recovery_key: string; escrowed: boolean; escrow_error?: string } | null>(null)
const typed = ref('')

async function create(): Promise<void> {
  busy.value = true
  error.value = null
  try {
    const r = await runOp(s.key.createTitle, () => api.backups.createKey({ recipients: list() }))
    created.value = r.data ?? null
    typed.value = ''
  } catch (e) {
    error.value = e instanceof ApiError ? e : new ApiError({ message: String(e) })
  } finally {
    busy.value = false
  }
}
async function escrow(): Promise<void> {
  error.value = null
  try {
    await runOp(s.key.escrowTitle, () => api.backups.escrowKey({ recipients: list() }))
    await l.reload()
  } catch (e) {
    error.value = e instanceof ApiError ? e : null
  }
}
function closeDialog(): void {
  created.value = null // forget the recovery key
  typed.value = ''
  void l.reload()
}
const tone = (st: string) => (st === 'escrowed' ? 'ok' : st === 'present' ? 'warn' : 'danger')
</script>

<template>
  <section class="panel" data-test="backup-key">
    <header><h2>{{ s.key.title }}</h2></header>
    <LoadState :loading="l.loading.value" :error="l.error.value" :has-data="!!l.data.value" @retry="l.reload">
      <div v-if="l.data.value" class="body stack">
        <div class="row"><span class="pill" :class="tone(l.data.value.status)" data-test="key-status">{{ s.key.status[l.data.value.status] }}</span><span v-if="l.data.value.domain" class="mono faint">{{ s.key.domain }} {{ l.data.value.domain }}</span></div>
        <p class="muted">{{ s.key.statusText[l.data.value.status] }}</p>
        <dl v-if="l.data.value.path" class="kv"><dt>{{ s.key.path }}</dt><dd class="mono">{{ l.data.value.path }}</dd></dl>
        <Callout tone="danger" data-test="key-loss">{{ s.key.loss }}</Callout>
        <div class="field" style="max-width: 520px"><label for="bk-rec">{{ s.key.recipients }}</label><input id="bk-rec" v-model="recipients" class="mono" type="text" placeholder="age1..." spellcheck="false" /><span class="hint">{{ s.key.recipientsHint }}</span></div>
        <ErrorPanel v-if="error && !created" :error="error" />
        <div class="row">
          <button v-if="l.data.value.status === 'none'" class="btn btn-primary" type="button" :disabled="busy || !admin" data-test="key-create" @click="create">{{ s.key.create }}</button>
          <button v-if="l.data.value.status === 'present'" class="btn btn-primary" type="button" :disabled="!admin" data-test="key-escrow" @click="escrow">{{ s.key.escrow }}</button>
          <span v-if="!admin" class="faint">{{ s.trust.notAllowed }}</span>
        </div>
      </div>
    </LoadState>
    <Modal :open="!!created" :title="s.key.shownTitle" wide @close="() => {}">
      <Callout tone="danger">{{ s.key.shownOnce }}</Callout>
      <div class="field" style="margin-top: 12px"><span class="label">{{ s.key.recovery }}</span><CopyField :value="created?.recovery_key ?? ''" :label="s.key.recovery" /></div>
      <Callout v-if="created && !created.escrowed" tone="warn" data-test="key-escrow-error">{{ s.key.notEscrowed }} {{ created.escrow_error }}</Callout>
      <label class="check" style="margin-top: 12px"><input v-model="typed" type="checkbox" true-value="yes" false-value="" data-test="key-saved" />{{ s.key.saved }}</label>
      <template #footer><button class="btn btn-primary" type="button" :disabled="typed !== 'yes'" data-test="key-done" @click="closeDialog">{{ s.access.done }}</button></template>
    </Modal>
  </section>
</template>
