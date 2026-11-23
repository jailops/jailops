<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { s } from '@/i18n'
import { api } from '@/api/endpoints'
import { ApiError } from '@jailops/shared/api/client'
import { useFormCheck } from '@jailops/shared/composables/form'
import { nameHint } from '@jailops/shared/lib/names'
import { nameError } from '@/lib/nameField'
import { capFirst } from '@/lib/rename'
import { ExportFileError, readManifest, scanFile } from '@/lib/exportfile'
import type { ExportManifest, JailImportRequest, Network, RestorePreflight } from '@jailops/shared/api/types'
import Modal from '@jailops/shared/components/Modal.vue'
import Callout from '@jailops/shared/components/Callout.vue'
import ErrorPanel from '@jailops/shared/components/ErrorPanel.vue'

// Import a jail (extension jail_export), admin in the project. The manifest at the start of the chosen file is read in
// the browser and sent to the preflight, which says what this host and project make of it; the file itself is read to its
// end (so a cut-off file is never sent) and then streamed to the daemon, which checks every stream and leaves nothing
// behind when anything is wrong.
const props = defineProps<{ open: boolean; networks: Network[] }>()
const emit = defineEmits<{ close: []; done: [name: string] }>()

const DEBOUNCE_MS = 300

const file = ref<File | null>(null)
const manifest = ref<ExportManifest | null>(null)
const fileProblem = ref('')
const as = ref('')
const keepIP = ref(false)
const start = ref(false)
const netMap = ref<Record<string, string>>({})
const pre = ref<RestorePreflight | null>(null)
const preFor = ref('')
const preError = ref<ApiError | null>(null)
const checking = ref(false)
const phase = ref<'' | 'scanning' | 'sending'>('')
const error = ref<ApiError | null>(null)
let seq = 0
let timer: ReturnType<typeof setTimeout> | undefined

const newName = computed(() => as.value || manifest.value?.name || '')
const check = useFormCheck(() => ({ 'im-name': manifest.value ? nameError(newName.value, 'object') : '' }))
const request = computed<JailImportRequest>(() => {
  const r: JailImportRequest = {}
  if (as.value) r.as = as.value
  if (keepIP.value) r.keep_ip = true
  if (start.value) r.start = true
  const m = Object.fromEntries(Object.entries(netMap.value).filter(([k, v]) => v && v !== k))
  if (Object.keys(m).length) r.network_map = m
  return r
})
const key = computed(() => JSON.stringify([manifest.value?.uuid, request.value]))
const fresh = computed(() => !!pre.value && preFor.value === key.value)
const networkNames = computed(() => manifest.value?.bundle.network_names ?? [])
const why = computed(() => {
  if (!file.value) return s.xport.needFile
  if (fileProblem.value) return fileProblem.value
  if (check.reason.value) return check.reason.value
  if (checking.value || !fresh.value) return s.xport.checking
  return pre.value?.ok ? '' : s.xport.cannot
})

watch(() => props.open, (o) => {
  if (!o) return
  file.value = null
  manifest.value = null
  fileProblem.value = ''
  as.value = ''
  keepIP.value = false
  start.value = false
  netMap.value = {}
  pre.value = null
  preFor.value = ''
  preError.value = null
  phase.value = ''
  error.value = null
  check.reset()
}, { immediate: true })

async function chosen(ev: Event): Promise<void> {
  const f = (ev.target as HTMLInputElement).files?.[0] ?? null
  file.value = f
  manifest.value = null
  fileProblem.value = ''
  pre.value = null
  error.value = null
  if (!f) return
  try {
    const m = await readManifest(f)
    manifest.value = m
    netMap.value = Object.fromEntries((m.bundle.network_names ?? []).map((n) => [n, n]))
  } catch (e) {
    fileProblem.value = e instanceof ExportFileError ? e.message : String(e)
  }
}

watch([manifest, key], () => {
  clearTimeout(timer)
  if (!manifest.value || nameError(newName.value, 'object')) return
  const m = manifest.value
  const k = key.value
  const r = request.value
  const mine = ++seq
  checking.value = true
  timer = setTimeout(async () => {
    try {
      const p = await api.xport.preflight({ manifest: m, request: r })
      if (mine !== seq) return
      pre.value = p
      preFor.value = k
      preError.value = null
    } catch (e) {
      if (mine !== seq) return
      pre.value = null
      preError.value = e instanceof ApiError ? e : new ApiError({ message: String(e) })
    } finally {
      if (mine === seq) checking.value = false
    }
  }, DEBOUNCE_MS)
})
onBeforeUnmount(() => clearTimeout(timer))

const go = () => check.submit(async () => {
  if (phase.value || !file.value || !manifest.value || !fresh.value || !pre.value?.ok) return
  error.value = null
  const name = newName.value
  try {
    phase.value = 'scanning'
    await scanFile(file.value)
    phase.value = 'sending'
    const got = await api.xport.import(file.value, request.value)
    emit('done', got.jail.metadata.name || name)
    emit('close')
  } catch (e) {
    if (e instanceof ExportFileError) fileProblem.value = s.xport.notWholeFile
    else error.value = e instanceof ApiError ? e : new ApiError({ message: String(e) })
  } finally {
    phase.value = ''
  }
})
</script>

<template>
  <Modal :open="open" :title="s.xport.importTitle" wide @close="phase || emit('close')">
    <form id="im" novalidate class="stack" data-test="import-form" data-tier="2" @submit.prevent="go">
      <p class="muted" style="margin: 0">{{ s.xport.importLead }}</p>
      <div class="field">
        <label for="im-file">{{ s.xport.file }} <span class="req" aria-hidden="true">*</span></label>
        <input id="im-file" type="file" :disabled="!!phase" data-test="import-file" aria-describedby="im-file-e" @change="chosen" />
        <span id="im-file-e" class="field-error" role="alert" data-test="import-file-error">{{ fileProblem }}</span>
      </div>
      <template v-if="manifest">
        <p data-test="import-summary">{{ s.xport.fileSummary(manifest.name, manifest.project) }}</p>
        <Callout v-if="manifest.warnings?.length" tone="warn" data-test="import-file-warnings">
          <ul class="im-list"><li v-for="w in manifest.warnings" :key="w">{{ capFirst(w) }}</li></ul>
        </Callout>
        <div class="field"><label for="im-name">{{ s.xport.newName }} <span class="req" aria-hidden="true">*</span></label>
          <input id="im-name" v-model="as" class="mono" type="text" autocomplete="off" spellcheck="false" :placeholder="manifest.name" :disabled="!!phase" :aria-invalid="!!check.err('im-name')" aria-describedby="im-name-h im-name-e" data-test="import-name" @input="check.touch('im-name')" />
          <span id="im-name-h" class="hint">{{ nameHint('object') }}</span>
          <span id="im-name-e" class="field-error" role="alert">{{ check.err('im-name') }}</span></div>
        <div v-for="n in networkNames" :key="n" class="field">
          <label :for="`im-net-${n}`">{{ s.xport.networkMap(n) }}</label>
          <select :id="`im-net-${n}`" v-model="netMap[n]" :disabled="!!phase" :data-test="`import-net-${n}`">
            <option :value="n">{{ n }} ({{ s.xport.sameName }})</option>
            <option v-for="x in networks.filter((y) => y.metadata.name !== n)" :key="x.metadata.name" :value="x.metadata.name">{{ x.metadata.name }}</option>
          </select>
        </div>
        <div class="field"><label class="check"><input v-model="keepIP" type="checkbox" :disabled="!!phase" data-test="import-keep-ip" /> {{ s.xport.keepIP }}</label></div>
        <div class="field"><label class="check"><input v-model="start" type="checkbox" :disabled="!!phase" data-test="import-start" /> {{ s.xport.start }}</label></div>
        <section :aria-label="s.xport.willDo" aria-live="polite" :aria-busy="checking" data-test="import-plan">
          <p v-if="checking && !fresh" class="muted" data-test="import-checking">{{ s.xport.checking }}...</p>
          <ErrorPanel v-else-if="preError" :error="preError" />
          <template v-else-if="pre">
            <template v-if="pre.missing?.length">
              <h4 class="im-h">{{ s.xport.missing }}</h4>
              <ul class="im-list"><li v-for="m in pre.missing" :key="m" data-test="import-missing">{{ capFirst(m) }}</li></ul>
            </template>
            <template v-if="pre.changes?.length">
              <h4 class="im-h">{{ s.xport.willDo }}</h4>
              <ul class="im-list"><li v-for="c in pre.changes" :key="c" data-test="import-change">{{ capFirst(c) }}</li></ul>
            </template>
            <Callout v-if="pre.errors?.length" tone="danger" data-test="import-errors">
              <strong>{{ s.xport.cannot }}</strong>
              <ul class="im-list"><li v-for="e in pre.errors" :key="e">{{ capFirst(e) }}</li></ul>
            </Callout>
          </template>
        </section>
      </template>
      <p v-else class="muted" data-test="import-choose">{{ s.xport.chooseFile }}</p>
      <p v-if="phase" class="muted" role="status" data-test="import-phase">{{ phase === 'scanning' ? s.xport.scanning : s.xport.sending }}...</p>
      <ErrorPanel v-if="error" :error="error" />
    </form>
    <template #footer>
      <span v-if="why" class="why-disabled" data-test="import-why">{{ why }}</span>
      <button class="btn" type="button" :disabled="!!phase" @click="emit('close')">{{ s.app.cancel }}</button>
      <button class="btn btn-primary" type="submit" form="im" :disabled="!!phase" :aria-disabled="!!why" data-test="import-go">{{ s.xport.go }}</button>
    </template>
  </Modal>
</template>

<style>
.im-h { margin: 8px 0 2px; font-size: 13px; font-weight: 600; }
.im-list { margin: 0; padding-left: 18px; display: grid; gap: 2px; }
.im-list li { overflow-wrap: anywhere; }
</style>
