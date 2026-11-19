<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { s } from '@/i18n'
import { api } from '@/api/endpoints'
import { ApiError } from '@jailops/shared/api/client'
import { bytes, snapshotLabel } from '@jailops/shared/lib/format'
import { checking, ExportFileError, fileName } from '@/lib/exportfile'
import type { JailSnapshot } from '@jailops/shared/api/types'
import Modal from '@jailops/shared/components/Modal.vue'
import Callout from '@jailops/shared/components/Callout.vue'
import ErrorPanel from '@jailops/shared/components/ErrorPanel.vue'

// Export a jail (extension jail_export), operator in the project. The file is written as it arrives: to a file the
// browser lets the page choose when it can (nothing is held in memory), else collected and offered as a download. The
// framing is checked on the way, so a file that ended early is said to be so here and not at the import.
const props = defineProps<{ open: boolean; jail: string; thick?: boolean; snapshots: JailSnapshot[]; hasVolumes?: boolean }>()
const emit = defineEmits<{ close: [] }>()

const snapshot = ref('')
const withVolumes = ref(false)
const zstd = ref(true)
const busy = ref(false)
const written = ref(0)
const error = ref<ApiError | null>(null)
const problem = ref('')
const result = ref('')

const sorted = computed(() => [...props.snapshots].sort((a, b) => (b.metadata.created_at ?? '').localeCompare(a.metadata.created_at ?? '')))

watch(() => props.open, (o) => {
  if (!o) return
  snapshot.value = ''
  withVolumes.value = false
  zstd.value = true
  written.value = 0
  error.value = null
  problem.value = ''
  result.value = ''
}, { immediate: true })

interface Picker {
  showSaveFilePicker?: (o: { suggestedName: string }) => Promise<{ createWritable(): Promise<WritableStream<Uint8Array>> }>
}

async function go(): Promise<void> {
  if (busy.value) return
  error.value = null
  problem.value = ''
  result.value = ''
  written.value = 0
  busy.value = true
  const name = fileName(props.jail)
  try {
    const picker = (window as unknown as Picker).showSaveFilePicker
    // the picker needs the click that started this: it is asked before the export is
    let sink: WritableStream<Uint8Array> | null = null
    if (picker) {
      try {
        sink = await (await picker({ suggestedName: name })).createWritable()
      } catch (e) {
        if ((e as Error).name === 'AbortError') {
          problem.value = s.xport.pickerCancelled
          return
        }
        throw e
      }
    }
    const body = await api.xport.export(props.jail, { snapshot: snapshot.value || undefined, withVolumes: withVolumes.value, zstd: zstd.value })
    const check = checking()
    const counted = new TransformStream<Uint8Array, Uint8Array>({ transform(chunk, ctl) { written.value += chunk.length; ctl.enqueue(chunk) } })
    const flow = body.pipeThrough(counted).pipeThrough(check.stream)
    if (sink) {
      await flow.pipeTo(sink)
      check.result()
    } else {
      const blob = await new Response(flow).blob()
      check.result()
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = name
      a.click()
      setTimeout(() => URL.revokeObjectURL(url), 10_000)
    }
    result.value = s.xport.saved(name, bytes(written.value))
  } catch (e) {
    if (e instanceof ExportFileError) problem.value = s.xport.notWhole
    else error.value = e instanceof ApiError ? e : new ApiError({ message: String(e) })
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <Modal :open="open" :title="s.xport.exportTitle(jail)" @close="busy || emit('close')">
    <form id="xp" class="stack" data-test="export-form" @submit.prevent="go">
      <p class="muted" style="margin: 0">{{ s.xport.exportLead }}</p>
      <p class="muted" style="margin: 0" data-test="export-layout">{{ thick ? s.xport.exportThick : s.xport.exportThin }}</p>
      <div v-if="sorted.length" class="field">
        <label for="xp-snap">{{ s.xport.snapshot }}</label>
        <select id="xp-snap" v-model="snapshot" :disabled="busy" data-test="export-snapshot">
          <option value="">{{ s.xport.now }}</option>
          <option v-for="x in sorted" :key="x.metadata.name" :value="x.metadata.name">{{ snapshotLabel(x.metadata.name) }}</option>
        </select>
      </div>
      <div v-if="hasVolumes" class="field">
        <label class="check"><input v-model="withVolumes" type="checkbox" :disabled="busy" data-test="export-volumes" /> {{ s.xport.withVolumes }}</label>
        <span class="hint">{{ s.xport.withVolumesHint }}</span>
      </div>
      <div class="field">
        <label class="check"><input v-model="zstd" type="checkbox" :disabled="busy" data-test="export-zstd" /> {{ s.xport.zstd }}</label>
      </div>
      <p v-if="busy" class="muted" role="status" data-test="export-progress">{{ s.xport.saving(bytes(written)) }}</p>
      <Callout v-if="result" tone="ok" data-test="export-done">{{ result }}</Callout>
      <Callout v-if="problem" tone="danger" data-test="export-problem">{{ problem }}</Callout>
      <ErrorPanel v-if="error" :error="error" />
    </form>
    <template #footer>
      <button class="btn" type="button" :disabled="busy" @click="emit('close')">{{ s.app.cancel }}</button>
      <button class="btn btn-primary" type="submit" form="xp" :disabled="busy" data-test="export-go">{{ s.xport.download }}</button>
    </template>
  </Modal>
</template>
