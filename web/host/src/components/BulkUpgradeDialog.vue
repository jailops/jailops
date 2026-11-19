<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { s } from '@/i18n'
import { api } from '@/api/endpoints'
import { ApiError } from '@jailops/shared/api/client'
import { runOp } from '@jailops/shared/composables/ops'
import type { Image, Jail } from '@jailops/shared/api/types'
import { commonMoveImages, readPreview, releaseChange } from '@/lib/moves'
import Modal from '@jailops/shared/components/Modal.vue'
import Callout from '@jailops/shared/components/Callout.vue'

// The selected jails move to another image (a new release), one at a time as `jailops jail upgrade NAME... --image IMAGE`
// does: every jail is checked first (a dry run each, with the release change), then the move runs for the jails that
// passed and the first failure stops the rest. A jail that cannot move says why and is left alone.
const props = defineProps<{ open: boolean; jails: Jail[] }>()
const emit = defineEmits<{ close: []; done: [] }>()

type Row = { jail: string; from: string; to: string; change: string; state: 'planned' | 'refused' | 'moved' | 'failed' | 'skipped'; message: string }
const images = ref<Image[]>([])
const image = ref('')
const rows = ref<Row[]>([])
const checkedFor = ref('')
const busy = ref(false)
const ran = ref(false)

// fixed when the dialog opens: the list reloads while the jails move, and a jail that has moved is no longer a candidate
const names = ref<string[]>([])
const candidates = ref<Image[]>([])
const fresh = computed(() => rows.value.length > 0 && checkedFor.value === image.value)
const planned = computed(() => rows.value.filter((r) => r.state === 'planned').length)
const refused = computed(() => rows.value.filter((r) => r.state === 'refused').length)

watch(() => props.open, async (o) => {
  if (!o) return
  image.value = ''
  rows.value = []
  checkedFor.value = ''
  ran.value = false
  names.value = props.jails.map((j) => j.metadata.name)
  try {
    images.value = await api.images.list()
  } catch {
    images.value = []
  }
  candidates.value = commonMoveImages(images.value, props.jails)
  image.value = candidates.value[0]?.metadata.name ?? ''
})
watch(image, () => { if (!ran.value) rows.value = [] })

const text = (e: unknown): string => (e instanceof ApiError ? e.message : String(e))

async function check(): Promise<void> {
  busy.value = true
  rows.value = []
  const out: Row[] = []
  for (const n of names.value) {
    try {
      const r = await runOp(s.upgrade.title(n), () => api.jails.upgrade(n, { image: image.value, dry_run: true }), { silent: true })
      const p = readPreview(r.data ?? r.op?.metadata, '')
      out.push({ jail: n, from: p.from_generation, to: p.to_generation, change: releaseChange(p), state: 'planned', message: (p.warnings ?? []).join(' ') })
    } catch (e) {
      out.push({ jail: n, from: '', to: '', change: '', state: 'refused', message: text(e) })
    }
    rows.value = [...out]
  }
  checkedFor.value = image.value
  busy.value = false
}

async function run(): Promise<void> {
  busy.value = true
  ran.value = true
  let stopped = ''
  for (const r of rows.value) {
    if (r.state !== 'planned') continue
    if (stopped) {
      r.state = 'skipped'
      r.message = s.bulkUpgrade.stoppedAfter(stopped)
      continue
    }
    try {
      await runOp(s.upgrade.title(r.jail), () => api.jails.upgrade(r.jail, { image: image.value }))
      r.state = 'moved'
    } catch (e) {
      r.state = 'failed'
      r.message = text(e)
      stopped = r.jail
    }
  }
  busy.value = false
  emit('done')
  if (!rows.value.some((r) => r.state === 'failed' || r.state === 'refused' || r.state === 'skipped')) emit('close')
}
</script>

<template>
  <Modal :open="open" :title="s.bulkUpgrade.title(jails.length)" wide @close="emit('close')">
    <p class="muted">{{ s.bulkUpgrade.lead }}</p>
    <Callout v-if="!candidates.length" tone="warn" data-test="bu-none">{{ s.bulkUpgrade.none }}</Callout>
    <template v-else>
      <div class="field">
        <label for="bu-image">{{ s.bulkUpgrade.image }}</label>
        <select id="bu-image" v-model="image" data-test="bu-image" :disabled="busy || ran">
          <option v-for="i in candidates" :key="i.metadata.name" :value="i.metadata.name">{{ i.metadata.name }} ({{ i.spec.release }})</option>
        </select>
      </div>
      <div class="row"><button class="btn" type="button" :disabled="busy || ran || !image" data-test="bu-check" @click="check">{{ s.bulkUpgrade.check }}</button></div>
      <table v-if="rows.length" class="table" style="margin-top: 12px" data-test="bu-rows">
        <thead><tr><th>{{ s.bulkUpgrade.jail }}</th><th>{{ s.bulkUpgrade.move }}</th><th>{{ s.bulkUpgrade.result }}</th></tr></thead>
        <tbody>
          <tr v-for="r in rows" :key="r.jail" :data-test="'bu-row-' + r.jail">
            <td class="mono">{{ r.jail }}</td>
            <td><span class="mono">{{ r.from || '?' }} &rarr; {{ r.to || '?' }}</span><br v-if="r.change" /><span v-if="r.change" class="muted">{{ r.change }}</span></td>
            <td :data-state="r.state">{{ s.bulkUpgrade.state[r.state] }}<span v-if="r.message" class="muted"> {{ r.message }}</span></td>
          </tr>
        </tbody>
      </table>
      <p v-if="refused" class="muted" data-test="bu-refused">{{ s.bulkUpgrade.refused(refused) }}</p>
    </template>
    <template #footer>
      <button class="btn" type="button" @click="emit('close')">{{ s.app.cancel }}</button>
      <button class="btn btn-primary" type="button" :disabled="!fresh || busy || ran || !planned" data-test="bu-run" @click="run">{{ s.bulkUpgrade.run(planned) }}</button>
    </template>
  </Modal>
</template>
