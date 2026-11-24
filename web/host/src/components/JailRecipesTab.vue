<script setup lang="ts">
import { computed } from 'vue'
import { s } from '@/i18n'
import { useRecipe } from '@/composables/recipes'
import { canApply } from '@/composables/shell'
import { projectState } from '@/composables/project'
import Callout from '@jailops/shared/components/Callout.vue'
import ErrorPanel from '@jailops/shared/components/ErrorPanel.vue'
import type { Jail } from '@jailops/shared/api/types'

// A recipe for a running FreeBSD jail (docs/provisioning.md): written or imported here, rendered in the browser, checked
// by the server (Validate), compared with the running jail (Dry run) and applied. The same recipe runs from a terminal
// with `jailops jail apply`. One editor, one form for the variables, no form per step.
const props = defineProps<{ jail: Jail }>()
const name = computed(() => props.jail.metadata.name)
const project = computed(() => props.jail.metadata.project || projectState.current)
const r = useRecipe(name)

const linux = computed(() => props.jail.spec.os === 'linux')
const running = computed(() => props.jail.status.power === 'running')
const mayRun = computed(() => canApply(props.jail.metadata.project))
// sync labels its jails (sync.jailops.net/...): the next run of sync puts them back as the fleet files say
const synced = computed(() => Object.keys(props.jail.metadata.labels ?? {}).some((k) => k.startsWith('sync.jailops.net/')))
const working = computed(() => r.busy.value !== '')
const runReason = computed(() => (!mayRun.value ? s.recipes.forbidden : linux.value ? s.recipes.linux : !running.value ? s.recipes.needsRunning : ''))
const canRun = computed(() => !runReason.value && !working.value && !!r.text.value.trim() && !r.declError.value)

const tone = (st: string) => (st === 'changed' ? 'ok' : st === 'failed' ? 'danger' : st.startsWith('would change') ? 'warn' : 'info')
const changed = computed(() => r.last.value?.result.steps.filter((x) => x.status === 'changed' || x.status === 'would change').length ?? 0)

function chosen(ev: Event): void {
  const el = ev.target as HTMLInputElement
  if (el.files?.length) void r.importFiles(el.files)
  el.value = ''
}
const cli = computed(() => `jailops jail apply ${name.value} -f DIR${project.value ? ` --project ${project.value}` : ''}`)
</script>

<template>
  <section class="panel" data-test="recipes-tab">
    <header><h2>{{ s.recipes.tab }}</h2></header>
    <div class="stack" style="padding: 0 16px 16px">
      <p class="muted">{{ s.recipes.lead }}</p>
      <Callout v-if="synced" tone="warn" data-test="recipes-sync">{{ s.recipes.sync }}</Callout>
      <Callout v-if="runReason" tone="info" data-test="recipes-why">{{ runReason }}</Callout>

      <div class="grid-2">
        <div class="field">
          <label for="r-files">{{ s.recipes.importFiles }}</label>
          <input id="r-files" type="file" multiple data-test="r-files" aria-describedby="r-files-hint" @change="chosen" />
          <span id="r-files-hint" class="hint faint">{{ s.recipes.importFilesHint }}</span>
        </div>
        <div class="field">
          <label for="r-dir">{{ s.recipes.importDir }}</label>
          <input id="r-dir" type="file" webkitdirectory data-test="r-dir" aria-describedby="r-dir-hint" @change="chosen" />
          <span id="r-dir-hint" class="hint faint">{{ s.recipes.importDirHint }}</span>
        </div>
      </div>
      <p v-if="r.note.value" class="faint" role="status" data-test="r-note">{{ r.note.value }}</p>

      <div class="field">
        <label for="r-text">{{ s.recipes.editor }}</label>
        <textarea id="r-text" v-model="r.text.value" class="mono" rows="14" spellcheck="false" autocomplete="off" autocapitalize="off" :placeholder="s.recipes.placeholder" aria-describedby="r-text-hint" data-test="r-text" />
        <span id="r-text-hint" class="hint faint">{{ s.recipes.editorHint }}</span>
      </div>

      <Callout v-if="r.declError.value" tone="warn" data-test="r-decl-problem">{{ s.recipes.declProblem }}: {{ r.declError.value }}</Callout>

      <fieldset v-if="r.decls.value.length" class="stack" data-test="r-vars">
        <legend>{{ s.recipes.variables }}</legend>
        <p class="muted">{{ s.recipes.variablesLead }}</p>
        <div v-for="d in r.decls.value" :key="d.name" class="field">
          <label :for="`r-var-${d.name}`">
            <span class="mono">{{ d.name }}</span>
            <span v-if="d.required" class="pill info" style="margin-left: 8px">{{ s.recipes.required }}</span>
            <span v-if="d.secret" class="pill warn" style="margin-left: 8px">{{ s.recipes.secret }}</span>
          </label>
          <input
            :id="`r-var-${d.name}`" v-model="r.form[d.name]" class="mono" :type="d.secret ? 'password' : 'text'"
            :autocomplete="d.secret ? 'new-password' : 'off'" spellcheck="false" :required="d.required" :aria-describedby="`r-var-${d.name}-hint`"
            :placeholder="d.default ?? ''" :data-test="`r-var-${d.name}`"
          />
          <span :id="`r-var-${d.name}-hint`" class="hint faint">{{ d.secret ? s.recipes.secretHint : d.default !== undefined ? s.recipes.defaultHint(d.default) : s.recipes.noDefault }}</span>
        </div>
      </fieldset>

      <div role="status" aria-live="polite" class="stack" data-test="r-status">
        <Callout v-if="r.problem.value" tone="danger" data-test="r-problem"><strong>{{ s.recipes.problem }}.</strong> {{ r.problem.value }}</Callout>
        <Callout v-if="r.valid.value !== null" tone="ok" data-test="r-valid">{{ s.recipes.valid(r.valid.value) }}</Callout>
        <ErrorPanel v-if="r.error.value" :error="r.error.value" />
      </div>

      <div class="row">
        <span v-if="runReason" class="why-disabled">{{ runReason }}</span>
        <button class="btn" type="button" :disabled="working || !r.text.value.trim() || !!r.declError.value" data-test="r-validate" @click="r.validate()">{{ s.recipes.validate }}</button>
        <button class="btn" type="button" :disabled="!canRun" data-test="r-dry" @click="r.run(true)">{{ s.recipes.dryRun }}</button>
        <button class="btn btn-primary" type="button" :disabled="!canRun" data-test="r-apply" @click="r.run(false)">{{ s.recipes.apply }}</button>
      </div>
      <p class="faint" style="margin: 0; font-size: 12px">{{ s.recipes.dryNote }}</p>

      <div role="status" aria-live="polite" data-test="r-results">
        <section v-if="r.last.value" class="stack" :aria-label="r.last.value.dry ? s.recipes.resultDry(name) : s.recipes.resultApply(name)">
          <h3>{{ r.last.value.dry ? s.recipes.resultDry(name) : s.recipes.resultApply(name) }}</h3>
          <p v-if="!r.last.value.result.steps.length" class="empty">{{ s.recipes.nothing }}</p>
          <template v-else>
            <p data-test="r-summary">{{ s.recipes.summary(changed, r.last.value.result.steps.length, r.last.value.dry) }}</p>
            <div class="table-wrap">
              <table data-test="r-steps">
                <thead><tr><th scope="col">{{ s.recipes.step }}</th><th scope="col">{{ s.recipes.status }}</th><th scope="col" class="hide-sm">{{ s.recipes.detail }}</th></tr></thead>
                <tbody>
                  <tr v-for="(st, i) in r.last.value.result.steps" :key="i" data-test="r-step">
                    <td><span class="faint">{{ i + 1 }}.</span> <strong>{{ st.kind }}</strong> <span class="mono">{{ st.name }}</span></td>
                    <td><span class="pill" :class="tone(st.status)">{{ st.status }}</span></td>
                    <td class="hide-sm faint">{{ st.detail }}</td>
                  </tr>
                </tbody>
              </table>
            </div>
          </template>
        </section>
        <p v-if="r.error.value?.code === 'step_failed'" class="faint" data-test="r-failed">{{ s.recipes.failedHint }}</p>
      </div>

      <p class="faint" style="margin: 0; font-size: 12px"><span>{{ s.recipes.cli }}:</span> <code class="mono">{{ cli }}</code></p>
    </div>
  </section>
</template>
