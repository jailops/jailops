<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { s } from '@/i18n'
import Modal from '@jailops/shared/components/Modal.vue'
import Callout from '@jailops/shared/components/Callout.vue'
import { runMany, tally, type RunResult } from '@/composables/runMany'
import type { Jail } from '@jailops/shared/api/types'

// One command line run in the selected jails that run, four at a time (`jailops exec -l KEY=VALUE -- sh -c LINE`).
// Each jail has its own block with its status; a stopped jail is named, not started.
const props = defineProps<{ open: boolean; jails: Jail[] }>()
const emit = defineEmits<{ close: [] }>()

const line = ref('')
const busy = ref(false)
const ran = ref(false)
const results = ref<RunResult[]>([])
const running = computed(() => props.jails.filter((j) => j.status.power === 'running'))
const skipped = computed(() => props.jails.filter((j) => j.status.power !== 'running').map((j) => j.metadata.name))
const sum = computed(() => tally(results.value))
const cli = computed(() => `jailops exec ${running.value.map((j) => j.metadata.name).join(' ')} -- sh -c ${JSON.stringify(line.value || '...')}`)

watch(() => props.open, (o) => {
  if (!o) return
  line.value = ''
  ran.value = false
  results.value = []
})

async function go(): Promise<void> {
  if (!line.value.trim() || busy.value || !running.value.length) return
  busy.value = true
  ran.value = true
  try {
    await runMany(running.value.map((j) => j.metadata.name), line.value, (r) => (results.value = r))
  } finally {
    busy.value = false
  }
}
const statusText = (r: RunResult) => (r.error ? s.runCmd.failedToStart : r.exit === undefined ? s.runCmd.running : s.runCmd.status(r.exit))
</script>

<template>
  <Modal :open="open" :title="s.runCmd.title(jails.length)" @close="emit('close')">
    <form id="run-cmd" class="stack" novalidate data-test="run-cmd" @submit.prevent="go">
      <div class="field">
        <label for="rc-command">{{ s.runCmd.command }}</label>
        <input id="rc-command" v-model="line" class="mono" type="text" autocomplete="off" spellcheck="false" autocapitalize="off" :placeholder="s.runCmd.placeholder" :disabled="busy" data-test="rc-command" />
        <span class="hint faint">{{ s.runCmd.hint }}</span>
      </div>
      <Callout v-if="skipped.length" tone="warn" data-test="rc-skipped">{{ s.runCmd.skipped(skipped.join(', ')) }}</Callout>
      <div v-if="ran" class="stack" aria-live="polite" data-test="rc-results">
        <section v-for="r in results" :key="r.jail" class="rc-jail" data-test="rc-result">
          <header class="row"><strong class="mono">{{ r.jail }}</strong> <span class="pill" :class="r.error || (r.exit !== undefined && r.exit !== 0) ? 'danger' : r.exit === 0 ? 'ok' : 'info'" data-test="rc-status">{{ statusText(r) }}</span></header>
          <pre class="rc-out mono" tabindex="0">{{ r.error || r.output || (r.exit === undefined ? '' : s.runCmd.noOutput) }}</pre>
        </section>
        <p v-if="!busy" class="muted" data-test="rc-summary">{{ s.runCmd.summary(sum.ok, sum.failed, skipped.length) }}</p>
      </div>
      <p class="faint rc-cli"><span>{{ s.runCmd.cli }}:</span> <code class="mono">{{ cli }}</code></p>
    </form>
    <template #footer>
      <button class="btn" type="button" @click="emit('close')">{{ s.runCmd.close }}</button>
      <button class="btn btn-primary" type="submit" form="run-cmd" :disabled="busy || !line.trim() || !running.length" data-test="rc-run">{{ busy ? s.runCmd.running : ran ? s.runCmd.again : s.runCmd.run }}</button>
    </template>
  </Modal>
</template>

<style>
.rc-jail { border: 1px solid var(--border); border-radius: 6px; padding: 8px 10px; }
.rc-jail header { gap: 8px; align-items: center; }
.rc-out { margin: 6px 0 0; max-height: 200px; overflow: auto; white-space: pre-wrap; word-break: break-word; font-size: 12px; }
.rc-cli { margin: 0; font-size: 12px; overflow-wrap: anywhere; }
</style>
