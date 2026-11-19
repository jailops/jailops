<script setup lang="ts">
// Installs a backup key made on another host. The secret is only in this form while it is open and is cleared on
// close; it goes to the daemon in the request body and nowhere else. The key is checked first: the dialog shows the key
// domain (not secret) so the operator can compare it with the one the old host showed, and imports only after that.
import { computed, ref, watch } from 'vue'
import { s } from '@/i18n'
import { api } from '@/api/endpoints'
import { ApiError } from '@jailops/shared/api/client'
import { runOp } from '@jailops/shared/composables/ops'
import Modal from '@jailops/shared/components/Modal.vue'
import ErrorPanel from '@jailops/shared/components/ErrorPanel.vue'
import Callout from '@jailops/shared/components/Callout.vue'
import type { BackupKeyImport, BackupKeyImported } from '@jailops/shared/api/types'

const props = defineProps<{ open: boolean; holds: boolean }>()
const emit = defineEmits<{ close: []; done: [] }>()

type Source = 'recovery' | 'file' | 'escrow'
const source = ref<Source>('recovery')
const recovery = ref('')
const keyFile = ref('')
const passphrase = ref('')
const identity = ref('')
const domain = ref('')
const skip = ref(false)
const replace = ref(false)
const busy = ref(false)
const error = ref<ApiError | null>(null)
const checked = ref<BackupKeyImported | null>(null)
const confirmed = ref(false)
const finished = ref<BackupKeyImported | null>(null)

function reset(): void {
  source.value = 'recovery'
  recovery.value = keyFile.value = passphrase.value = identity.value = domain.value = ''
  skip.value = replace.value = confirmed.value = false
  error.value = null
  checked.value = null
  finished.value = null
}
watch(() => props.open, reset)
// a changed input invalidates the check that was made for the old one
watch([source, recovery, keyFile, passphrase, identity, domain, skip, replace], () => {
  checked.value = null
  confirmed.value = false
})

const filled = computed(() => (source.value === 'recovery' ? !!recovery.value.trim() : source.value === 'file' ? !!keyFile.value.trim() : !!identity.value.trim()))
const body = (check: boolean): BackupKeyImport => ({
  ...(source.value === 'recovery' ? { recovery_key: recovery.value.trim() } : {}),
  ...(source.value === 'file' ? { key_file: keyFile.value.trim(), ...(passphrase.value ? { passphrase: passphrase.value } : {}) } : {}),
  ...(source.value === 'escrow' ? { identity: identity.value.trim(), ...(domain.value.trim() ? { domain: domain.value.trim() } : {}) } : {}),
  ...(replace.value ? { replace: true } : {}),
  ...(skip.value ? { skip_node_check: true } : {}),
  ...(check ? { check: true } : {}),
})

async function check(): Promise<void> {
  busy.value = true
  error.value = null
  checked.value = null
  try {
    const r = await api.backups.importKey(body(true))
    checked.value = r.kind === 'sync' ? r.data : null
  } catch (e) {
    error.value = e instanceof ApiError ? e : new ApiError({ message: String(e) })
  } finally {
    busy.value = false
  }
}

async function doImport(): Promise<void> {
  busy.value = true
  error.value = null
  try {
    const r = await runOp(s.key.importOp, () => api.backups.importKey(body(false)))
    finished.value = r.data ?? null
    recovery.value = keyFile.value = passphrase.value = identity.value = '' // forget the secret
    emit('done')
  } catch (e) {
    error.value = e instanceof ApiError ? e : new ApiError({ message: String(e) })
  } finally {
    busy.value = false
  }
}

function load(ev: Event): void {
  const f = (ev.target as HTMLInputElement).files?.[0]
  if (!f) return
  if (f.size > 64 * 1024) {
    error.value = new ApiError({ message: s.key.fileHint })
    return
  }
  void f.text().then((t) => { keyFile.value = t })
}

// what an import would do, in a sentence
const outcome = computed(() => {
  const r = checked.value
  if (!r) return ''
  if (r.existing && r.existing === r.domain) return s.key.outcome.held
  return r.existing ? s.key.outcome.replace : s.key.outcome.install
})
const repo = computed(() => {
  const r = checked.value
  if (!r) return ''
  if (r.verified) return s.key.repoMatches
  return r.repository ? r.repository : skip.value ? s.key.repoNotChecked : s.key.repoNone
})
const needsReplace = computed(() => !!checked.value && !!checked.value.existing && checked.value.existing !== checked.value.domain && !replace.value)
const held = computed(() => !!checked.value && !!checked.value.existing && checked.value.existing === checked.value.domain)
function close(): void {
  reset()
  emit('close')
}
</script>

<template>
  <Modal :open="open" :title="s.key.importTitle" wide @close="close">
    <div v-if="finished" class="stack" data-test="key-import-done">
      <Callout tone="ok">{{ finished.installed ? s.key.imported(finished.backups ?? 0) : s.key.notImported }}</Callout>
      <Callout v-if="finished.warning" tone="warn">{{ finished.warning }}</Callout>
      <dl class="kv"><dt>{{ s.key.domain }}</dt><dd class="mono">{{ finished.domain }}</dd></dl>
    </div>
    <form v-else id="bki" novalidate @submit.prevent="check">
      <p class="muted">{{ s.key.importLead }}</p>
      <fieldset class="field" style="border: 0; padding: 0">
        <legend class="label">{{ s.key.importSource }}</legend>
        <div class="row">
          <label v-for="k in (['recovery', 'file', 'escrow'] as Source[])" :key="k" class="check"><input v-model="source" type="radio" name="bki-source" :value="k" :data-test="`key-source-${k}`" />{{ s.key.sources[k] }}</label>
        </div>
      </fieldset>
      <div v-if="source === 'recovery'" class="field">
        <label for="bki-rec">{{ s.key.recoveryField }}</label>
        <input id="bki-rec" v-model="recovery" class="mono" type="password" autocomplete="off" spellcheck="false" aria-describedby="bki-rec-h" data-test="key-recovery" />
        <span id="bki-rec-h" class="hint">{{ s.key.recoveryHint }}</span>
      </div>
      <template v-if="source === 'file'">
        <div class="field">
          <label for="bki-file">{{ s.key.fileField }}</label>
          <textarea id="bki-file" v-model="keyFile" class="mono" rows="5" autocomplete="off" spellcheck="false" aria-describedby="bki-file-h" data-test="key-file"></textarea>
          <span id="bki-file-h" class="hint">{{ s.key.fileHint }}</span>
          <label class="btn" style="width: fit-content">{{ s.key.fileLoad }}<input type="file" hidden data-test="key-file-load" @change="load" /></label>
        </div>
        <div class="field">
          <label for="bki-pass">{{ s.key.passphraseField }}</label>
          <input id="bki-pass" v-model="passphrase" type="password" autocomplete="off" data-test="key-passphrase" />
        </div>
      </template>
      <template v-if="source === 'escrow'">
        <div class="field">
          <label for="bki-id">{{ s.key.identityField }}</label>
          <input id="bki-id" v-model="identity" class="mono" type="password" autocomplete="off" spellcheck="false" aria-describedby="bki-id-h" data-test="key-identity" />
          <span id="bki-id-h" class="hint">{{ s.key.identityHint }}</span>
        </div>
        <div class="field">
          <label for="bki-dom">{{ s.key.domainField }}</label>
          <input id="bki-dom" v-model="domain" class="mono" type="text" autocomplete="off" aria-describedby="bki-dom-h" />
          <span id="bki-dom-h" class="hint">{{ s.key.domainHint }}</span>
        </div>
      </template>
      <label class="check"><input v-model="skip" type="checkbox" data-test="key-skip-check" />{{ s.key.skipCheck }}</label>
      <span class="hint">{{ s.key.skipHint }}</span>
      <template v-if="holds">
        <label class="check"><input v-model="replace" type="checkbox" data-test="key-replace" />{{ s.key.replaceKey }}</label>
        <span class="hint">{{ s.key.replaceHint }}</span>
      </template>
      <ErrorPanel v-if="error" :error="error" style="margin-top: 12px" />
      <section v-if="checked" class="panel" style="margin-top: 12px" data-test="key-import-result" aria-live="polite">
        <header><h3>{{ s.key.resultTitle }}</h3></header>
        <dl class="kv body">
          <dt>{{ s.key.resultDomain }}</dt><dd class="mono" data-test="key-import-domain">{{ checked.domain }}</dd>
          <dt>{{ s.key.resultRepo }}</dt><dd :class="{ mono: !!checked.repository }" data-test="key-import-repo">{{ repo }}</dd>
          <template v-if="checked.existing"><dt>{{ s.key.resultExisting }}</dt><dd class="mono">{{ checked.existing }}</dd></template>
        </dl>
        <p class="body" data-test="key-import-outcome">{{ outcome }}</p>
        <Callout v-if="needsReplace" tone="warn" data-test="key-import-needs-replace">{{ s.key.needReplace }}</Callout>
        <label v-if="!held" class="check body"><input v-model="confirmed" type="checkbox" data-test="key-import-confirm" />{{ s.key.confirmDomain }}</label>
      </section>
    </form>
    <template #footer>
      <button class="btn" type="button" @click="close">{{ finished ? s.key.importDone : s.app.cancel }}</button>
      <template v-if="!finished">
        <button class="btn" type="submit" form="bki" :disabled="busy || !filled" data-test="key-import-check">{{ s.key.check }}</button>
        <button class="btn btn-primary" type="button" :disabled="busy || !checked || held || needsReplace || !confirmed" data-test="key-import-go" @click="doImport">{{ s.key.importGo }}</button>
      </template>
    </template>
  </Modal>
</template>
