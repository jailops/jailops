<script setup lang="ts">
// An in-place upgrade of the jail: its FreeBSD base from the host and its packages inside it, while it runs. Nothing
// stops or restarts the jail; a snapshot taken first is the point a manual rollback returns to.
import { computed, ref, watch } from 'vue'
import { s } from '@/i18n'
import { api } from '@/api/endpoints'
import { ApiError } from '@jailops/shared/api/client'
import { runOp } from '@jailops/shared/composables/ops'
import type { Jail } from '@jailops/shared/api/types'
import type { UpgradePreview } from '@/types'
import { readPreview, releaseChange } from '@/lib/upgrade'
import Modal from '@jailops/shared/components/Modal.vue'
import Callout from '@jailops/shared/components/Callout.vue'
import ErrorPanel from '@jailops/shared/components/ErrorPanel.vue'

const props = defineProps<{ open: boolean; jail: Jail }>()
const emit = defineEmits<{ close: []; done: [] }>()

const release = ref('') // empty: the patch updates of the installed release
const preview = ref<UpgradePreview | null>(null)
const previewFor = ref<string | null>(null)
const busy = ref(false)
const error = ref<ApiError | null>(null)

const key = computed(() => release.value.trim())
const fresh = computed(() => !!preview.value && previewFor.value === key.value)
const change = computed(() => (preview.value ? releaseChange(preview.value) : ''))
const body = (dry: boolean) => ({ release: key.value || undefined, ...(dry ? { dry_run: true } : {}) })

watch(() => props.open, (o) => {
  if (!o) return
  release.value = ''
  preview.value = null
  previewFor.value = null
  error.value = null
})

async function dry(): Promise<void> {
  busy.value = true
  error.value = null
  try {
    const r = await runOp(s.upgrade.title(props.jail.metadata.name), () => api.jails.upgrade(props.jail.metadata.name, body(true)), { silent: true })
    preview.value = readPreview(r.data ?? r.op?.metadata)
    previewFor.value = key.value
  } catch (e) {
    error.value = e instanceof ApiError ? e : new ApiError({ message: String(e) })
  } finally {
    busy.value = false
  }
}

async function go(): Promise<void> {
  busy.value = true
  error.value = null
  try {
    await runOp(s.upgrade.title(props.jail.metadata.name), () => api.jails.upgrade(props.jail.metadata.name, body(false)))
    emit('done')
    emit('close')
  } catch (e) {
    error.value = e instanceof ApiError ? e : new ApiError({ message: String(e) })
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <Modal :open="open" :title="s.upgrade.title(jail.metadata.name)" wide @close="emit('close')">
    <p class="muted">{{ s.upgrade.lead }}</p>
    <div class="field">
      <label for="up-release">{{ s.upgrade.target }}</label>
      <input id="up-release" v-model="release" type="text" class="mono" :placeholder="s.upgrade.samePlaceholder" data-test="up-release" />
      <span class="hint">{{ s.upgrade.targetHint }}</span>
    </div>
    <div class="row"><button class="btn" type="button" :disabled="busy" data-test="preview" @click="dry">{{ busy && !fresh ? s.upgrade.previewing : s.upgrade.preview }}</button></div>
    <div v-if="preview" class="stack" style="margin-top: 12px" data-test="preview-result">
      <p v-if="!fresh" class="muted">{{ s.upgrade.stale }}</p>
      <p v-if="preview.nothing" data-test="preview-nothing">{{ s.upgrade.nothing(preview.installed ?? '') }}</p>
      <template v-else>
        <p v-if="change" data-test="preview-release">{{ s.upgrade.release(change) }}</p>
        <p v-if="preview.snapshot" class="muted">{{ s.upgrade.rollbackPoint(preview.snapshot) }}</p>
        <div v-if="preview.base_changes?.length" data-test="preview-base">
          <h3>{{ s.upgrade.baseChanges }}</h3>
          <ul class="mono"><li v-for="c in preview.base_changes" :key="c">{{ c }}</li></ul>
        </div>
        <div v-if="preview.ports_packages?.length" data-test="preview-ports">
          <h3>{{ s.upgrade.portsPackages }}</h3>
          <ul class="mono"><li v-for="c in preview.ports_packages" :key="c">{{ c }}</li></ul>
        </div>
        <div v-if="preview.steps.length">
          <h3>{{ s.upgrade.plan }}</h3>
          <ol class="steps"><li v-for="st in preview.steps" :key="st">{{ st }}</li></ol>
        </div>
        <p v-else-if="!preview.base_changes?.length && !preview.ports_packages?.length" class="muted">{{ s.upgrade.noPreview }}</p>
        <p v-if="preview.health_check" class="muted">{{ s.upgrade.healthCheck(preview.health_check) }}</p>
      </template>
      <Callout v-if="preview.failed_checks?.length" tone="danger">
        <strong>{{ s.upgrade.failedChecks }}</strong>
        <ul><li v-for="c in preview.failed_checks" :key="c">{{ c }}</li></ul>
      </Callout>
      <Callout v-if="preview.warnings?.length" tone="warn">
        <strong>{{ s.upgrade.warnings }}</strong>
        <ul><li v-for="c in preview.warnings" :key="c">{{ c }}</li></ul>
      </Callout>
    </div>
    <p v-else class="muted" style="margin-top: 12px">{{ s.upgrade.needPreview }}</p>
    <ErrorPanel v-if="error" :error="error" style="margin-top: 12px" />
    <template #footer>
      <span v-if="!fresh" class="why-disabled">{{ s.upgrade.needPreview }}</span>
      <button class="btn" type="button" @click="emit('close')">{{ s.app.cancel }}</button>
      <button class="btn btn-primary" type="button" :disabled="!fresh || busy || !!preview?.nothing || !!preview?.failed_checks?.length" data-test="upgrade-go" @click="go">{{ s.upgrade.run }}</button>
    </template>
  </Modal>
</template>

<style>
.steps { margin: 4px 0 0; padding-left: 20px; }
</style>
