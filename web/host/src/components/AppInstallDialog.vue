<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { s } from '@/i18n'
import { api } from '@/api/endpoints'
import { ApiError } from '@jailops/shared/api/client'
import { runOp } from '@jailops/shared/composables/ops'
import { projectState } from '@/composables/project'
import { useInstallGate } from '@/composables/apps'
import { initialForm, installBody, paragraphs, parseStep, stepState, stepsDone, validateInstall, openLinks, INSTALL_SNAPSHOT, type InstallForm } from '@/lib/apps'
import { logFileOf } from '@/lib/logs'
import Modal from '@jailops/shared/components/Modal.vue'
import Callout from '@jailops/shared/components/Callout.vue'
import ErrorPanel from '@jailops/shared/components/ErrorPanel.vue'
import CopyField from '@jailops/shared/components/CopyField.vue'
import AddrCopy from '@jailops/shared/components/AddrCopy.vue'
import Icon from '@jailops/shared/components/Icon.vue'
import StatePill from '@/components/StatePill.vue'
import type { AppDetail, AppInstallResult } from '@jailops/shared/api/types'

const props = defineProps<{ open: boolean; detail: AppDetail | null }>()
const emit = defineEmits<{ close: []; done: [] }>()

const gate = useInstallGate()
const app = computed(() => props.detail?.definition)
const title = computed(() => app.value?.title || app.value?.name || '')

const f = reactive<InstallForm>({ jail: '', network: '', storage: '', memory: '', disk: '', cpu: '' })
const taken = ref<string[]>([])
const touched = ref(false)
type Phase = 'form' | 'running' | 'done' | 'failed'
const phase = ref<Phase>('form')
const busy = ref(false)
const error = ref<ApiError | null>(null)
const steps = ref<string[]>([])
const stepsFor = ref('')
const progress = ref(0)
const result = ref<AppInstallResult | null>(null)

const issues = computed(() => validateInstall(f, { networks: gate.granted.value.networks, storage: gate.granted.value.storage, taken: taken.value }))
const valid = computed(() => !Object.keys(issues.value).length)
const issueText = (k: keyof InstallForm): string => {
  const i = issues.value[k]
  return i && (touched.value || k === 'jail' || k === 'memory' || k === 'disk' || k === 'cpu') ? s.install[i] : ''
}
const body = (dry = false) => installBody(f, app.value?.resources, dry)
const key = computed(() => JSON.stringify(body()))
const fresh = computed(() => !!steps.value.length && stepsFor.value === key.value)
const parsed = computed(() => steps.value.map(parseStep))
const done = computed(() => stepsDone(progress.value, parsed.value.length, phase.value === 'done'))
const runState = computed<'running' | 'success' | 'failure'>(() => (phase.value === 'done' ? 'success' : phase.value === 'failed' ? 'failure' : 'running'))
const failedStep = computed(() => error.value?.detail?.step ?? '')
const kept = computed(() => error.value?.detail?.jail_kept === 'true')
const current = computed(() => parsed.value[Math.min(done.value, parsed.value.length - 1)])
const links = computed(() => (result.value ? openLinks(result.value.jail, result.value.ports ?? []) : []))
const rollbackCmd = computed(() => `jailops jail rollback ${result.value?.jail.metadata.name ?? f.jail} --all --snapshot ${result.value?.snapshot || INSTALL_SNAPSHOT}`)

watch(() => props.open, (o) => {
  if (!o || !app.value) return
  Object.assign(f, initialForm(app.value.name, app.value.resources, gate.granted.value.networks, gate.granted.value.storage))
  phase.value = 'form'
  error.value = null
  steps.value = []
  stepsFor.value = ''
  progress.value = 0
  result.value = null
  touched.value = false
  taken.value = []
  if (projectState.current) api.jails.list().then((l) => (taken.value = l.map((j) => j.metadata.name))).catch(() => undefined)
})

/** The dry run: the server answers with the exact steps it would take, and does nothing. */
async function preview(): Promise<boolean> {
  touched.value = true
  if (!valid.value || !app.value) return false
  busy.value = true
  error.value = null
  try {
    const want = key.value
    const r = await runOp(s.install.title(title.value), () => api.apps.install(app.value!.name, body(true), projectState.current), { silent: true })
    const res = (r.data ?? r.op?.metadata) as AppInstallResult | undefined
    steps.value = res?.steps ?? []
    stepsFor.value = want
    return true
  } catch (e) {
    error.value = e instanceof ApiError ? e : new ApiError({ message: String(e) })
    return false
  } finally {
    busy.value = false
  }
}

async function install(): Promise<void> {
  touched.value = true
  if (!valid.value || !app.value) return
  // The step list comes from the dry run, so a progress figure can be shown against named steps.
  if (!fresh.value && !(await preview())) return
  busy.value = true
  error.value = null
  progress.value = 0
  phase.value = 'running'
  const jail = f.jail
  try {
    const r = await runOp(`${s.install.title(title.value)}: ${jail}`, () => api.apps.install(app.value!.name, body(), projectState.current), {
      poll: async (id) => {
        const op = await api.operations.wait(id)
        progress.value = op.progress ?? progress.value
        return op
      },
    })
    const res = (r.op?.metadata ?? r.data) as AppInstallResult | undefined
    if (res?.steps?.length) steps.value = res.steps
    result.value = res ?? null
    phase.value = 'done'
  } catch (e) {
    error.value = e instanceof ApiError ? e : new ApiError({ message: String(e) })
    phase.value = 'failed'
  } finally {
    busy.value = false
    emit('done')
  }
}

function back(): void {
  phase.value = 'form'
  error.value = null
  progress.value = 0
}
</script>

<template>
  <Modal :open="open" :title="s.install.title(title)" wide @close="emit('close')">
    <div v-if="app" class="stack" data-test="install-dialog" :data-phase="phase">
      <template v-if="phase === 'form'">
        <Callout v-if="gate.reason.value" tone="warn" data-test="install-blocked">{{ gate.reason.value }} <router-link v-if="gate.toProject.value" :to="gate.toProject.value" @click="emit('close')">{{ s.apps.toProject }}</router-link></Callout>
        <p v-else-if="projectState.current" class="muted">{{ s.install.lead(projectState.current) }}</p>
        <form id="inst" class="inst-form" novalidate @submit.prevent="install">
          <div class="field inst-wide">
            <label for="inst-jail">{{ s.install.jail }}</label>
            <input id="inst-jail" v-model.trim="f.jail" class="mono" type="text" autocomplete="off" spellcheck="false" :aria-invalid="!!issueText('jail')" :aria-describedby="issueText('jail') ? 'inst-jail-err' : 'inst-jail-hint'" />
            <span v-if="issueText('jail')" id="inst-jail-err" class="field-error" role="alert">{{ issueText('jail') }}</span>
            <span v-else id="inst-jail-hint" class="hint">{{ s.install.jailHint }}</span>
          </div>
          <div class="field">
            <label for="inst-net">{{ s.install.network }}</label>
            <select id="inst-net" v-model="f.network" :aria-invalid="!!issueText('network')" :aria-describedby="issueText('network') ? 'inst-net-err' : undefined" data-test="install-network">
              <option v-if="gate.granted.value.networks.length !== 1" value="">{{ s.install.choose }}</option>
              <option v-for="n in gate.granted.value.networks" :key="n" :value="n">{{ n }}</option>
            </select>
            <span v-if="issueText('network')" id="inst-net-err" class="field-error">{{ issueText('network') }}</span>
            <span v-else-if="gate.granted.value.networks.length === 1" class="hint">{{ s.install.onlyGrant }}</span>
          </div>
          <div class="field">
            <label for="inst-sto">{{ s.install.storage }}</label>
            <select id="inst-sto" v-model="f.storage" :aria-invalid="!!issueText('storage')" :aria-describedby="issueText('storage') ? 'inst-sto-err' : undefined" data-test="install-storage">
              <option v-if="gate.granted.value.storage.length !== 1" value="">{{ s.install.choose }}</option>
              <option v-for="n in gate.granted.value.storage" :key="n" :value="n">{{ n }}</option>
            </select>
            <span v-if="issueText('storage')" id="inst-sto-err" class="field-error">{{ issueText('storage') }}</span>
            <span v-else-if="gate.granted.value.storage.length === 1" class="hint">{{ s.install.onlyGrant }}</span>
          </div>
          <fieldset class="inst-limits inst-wide">
            <legend>{{ s.install.limits }}</legend>
            <p class="hint">{{ s.install.limitsHint }}</p>
            <div class="inst-limit-row">
              <div class="field"><label for="inst-mem">{{ s.install.memory }}</label><input id="inst-mem" v-model="f.memory" class="mono" type="text" autocomplete="off" :aria-invalid="!!issueText('memory')" /><span v-if="issueText('memory')" class="field-error">{{ issueText('memory') }}</span></div>
              <div class="field"><label for="inst-disk">{{ s.install.disk }}</label><input id="inst-disk" v-model="f.disk" class="mono" type="text" autocomplete="off" :aria-invalid="!!issueText('disk')" /><span v-if="issueText('disk')" class="field-error">{{ issueText('disk') }}</span></div>
              <div class="field"><label for="inst-cpu">{{ s.install.cpu }}</label><input id="inst-cpu" v-model="f.cpu" class="mono" type="text" inputmode="decimal" autocomplete="off" :aria-invalid="!!issueText('cpu')" /><span v-if="issueText('cpu')" class="field-error">{{ issueText('cpu') }}</span></div>
            </div>
          </fieldset>
        </form>
        <section v-if="steps.length" class="inst-plan" data-test="install-plan" aria-labelledby="inst-plan-h">
          <h3 id="inst-plan-h">{{ s.install.steps }}</h3>
          <p v-if="!fresh" class="muted">{{ s.install.stale }}</p>
          <p v-else class="faint">{{ s.install.stepsLead }}</p>
          <ol class="inst-steps">
            <li v-for="st in parsed" :key="st.n + st.name" data-state="pending"><span class="inst-n mono">{{ String(st.n).padStart(2, '0') }}</span><div class="grow"><span class="mono inst-name">{{ st.name }}</span><span class="inst-text mono">{{ st.text }}</span></div></li>
          </ol>
        </section>
        <ErrorPanel v-if="error" :error="error" />
      </template>

      <template v-else>
        <p v-if="phase === 'running'" class="inst-status" role="status" data-test="install-status">
          <span class="pill info pulse">{{ s.install.installing }}</span>
          <span>{{ current ? s.install.progress(Math.min(done + 1, parsed.length), parsed.length, current.name) : s.install.waiting }}</span>
        </p>
        <div v-if="phase === 'running'" class="meter" aria-hidden="true"><span :style="{ width: Math.max(3, progress) + '%' }"></span></div>

        <template v-if="phase === 'done' && result">
          <Callout tone="ok" data-test="install-ok"><strong>{{ s.install.okTitle(title, result.jail.metadata.name) }}</strong></Callout>
          <dl class="kv">
            <dt>{{ s.apps.jail }}</dt><dd><router-link class="mono" :to="`/jails/${result.jail.metadata.name}`" data-test="install-jail-link" @click="emit('close')">{{ result.jail.metadata.name }}</router-link></dd>
            <dt>{{ s.install.state }}</dt><dd><StatePill :state="result.jail.status.power" /></dd>
            <dt v-if="result.version">{{ s.install.version }}</dt><dd v-if="result.version" class="mono">{{ result.version }}</dd>
            <dt>{{ s.apps.addresses }}</dt><dd><span v-if="!result.addresses?.length" class="faint">-</span><span v-for="a in result.addresses" :key="a" class="inst-addr"><AddrCopy :value="a" /></span></dd>
          </dl>
          <section v-if="result.ports?.length" aria-labelledby="inst-ports-h">
            <h3 id="inst-ports-h">{{ s.install.endpoints }}</h3>
            <div class="table-wrap">
              <table data-test="install-ports">
                <thead><tr><th>{{ s.apps.port }}</th><th>{{ s.install.endpoint }}</th><th class="hide-sm">&nbsp;</th></tr></thead>
                <tbody>
                  <tr v-for="p in result.ports" :key="p.port + p.proto">
                    <td class="nowrap"><span class="mono">{{ p.name }}</span> <span class="faint mono">{{ p.port }}/{{ p.proto }}</span></td>
                    <td class="nowrap"><AddrCopy v-if="p.endpoint" :value="p.endpoint" /><span v-else class="faint">-</span></td>
                    <td class="hide-sm nowrap">
                      <span v-if="p.expose" class="badge info">{{ s.apps.exposed }}</span>
                      <a v-for="l in links.filter((x) => x.port.port === p.port)" :key="l.url" class="btn btn-sm inst-open" :href="l.url" target="_blank" rel="noopener noreferrer" :title="s.apps.openTitle(l.url)"><Icon name="externalLink" />{{ s.apps.open }}</a>
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
          </section>
          <section aria-labelledby="inst-where-h">
            <h3 id="inst-where-h">{{ s.install.where }}</h3>
            <dl class="kv inst-where">
              <template v-if="result.data?.length"><dt>{{ s.apps.data }}</dt><dd><div v-for="d in result.data" :key="d.path"><span class="mono">{{ d.path }}</span></div></dd></template>
              <template v-if="result.config?.length"><dt>{{ s.apps.config }}</dt><dd><div v-for="c in result.config" :key="c.path"><span class="mono">{{ c.path }}</span><span v-if="c.restart" class="faint"> · {{ s.apps.restarts(c.restart) }}</span></div></dd></template>
              <template v-if="result.logs?.length"><dt>{{ s.apps.logs }}</dt><dd><div v-for="g in result.logs" :key="g.name"><router-link v-if="logFileOf(g.path)" class="mono" :to="{ path: `/jails/${result.jail.metadata.name}`, query: { tab: 'logs', file: logFileOf(g.path) } }" @click="emit('close')">{{ g.path }}</router-link><span v-else class="mono">{{ g.path }}</span></div></dd></template>
            </dl>
          </section>
          <section v-if="result.notes" aria-labelledby="inst-notes-h">
            <h3 id="inst-notes-h">{{ s.apps.notes }}</h3>
            <div class="inst-notes"><p v-for="(p, i) in paragraphs(result.notes)" :key="i">{{ p }}</p></div>
          </section>
          <section aria-labelledby="inst-rb-h" data-test="install-rollback">
            <h3 id="inst-rb-h">{{ s.install.rollbackTitle }}</h3>
            <p class="muted">{{ s.install.rollbackText(result.snapshot || INSTALL_SNAPSHOT) }}</p>
            <CopyField :value="rollbackCmd" :label="s.install.command" />
          </section>
        </template>

        <template v-if="phase === 'failed' && error">
          <ErrorPanel :error="error" />
          <Callout v-if="kept" tone="warn" data-test="install-kept">{{ s.install.kept(error.detail.jail || f.jail) }} <router-link :to="`/jails/${error.detail.jail || f.jail}`" @click="emit('close')">{{ s.install.openJail }}</router-link></Callout>
          <p v-else-if="error.detail?.step" class="muted">{{ s.install.notKept }}</p>
        </template>

        <section v-if="parsed.length" class="inst-plan" aria-labelledby="inst-run-h">
          <h3 id="inst-run-h">{{ s.install.steps }}</h3>
          <ol class="inst-steps" data-test="install-steps">
            <li v-for="(st, i) in parsed" :key="st.n + st.name" :data-state="stepState(i, parsed, done, runState, failedStep)" :aria-current="stepState(i, parsed, done, runState, failedStep) === 'running' ? 'step' : undefined">
              <Icon :name="({ done: 'checkCircle', running: 'rotateCw', pending: 'circle', failed: 'alertCircle' } as const)[stepState(i, parsed, done, runState, failedStep)]" />
              <div class="grow"><span class="mono inst-name">{{ st.name }}</span><span class="sr-only"> ({{ s.install.stepState[stepState(i, parsed, done, runState, failedStep)] }})</span><span class="inst-text mono">{{ st.text }}</span></div>
            </li>
          </ol>
        </section>
      </template>
    </div>
    <template #footer>
      <template v-if="phase === 'form'">
        <button class="btn" type="button" @click="emit('close')">{{ s.app.cancel }}</button>
        <button class="btn" type="button" :disabled="busy || !!gate.block.value" data-test="install-preview" @click="preview">{{ busy ? s.install.previewing : s.install.preview }}</button>
        <button class="btn btn-primary" type="submit" form="inst" :disabled="busy || !valid || !!gate.block.value" data-test="install-go"><Icon name="pkg" />{{ s.install.go }}</button>
      </template>
      <template v-else-if="phase === 'running'">
        <button class="btn" type="button" @click="emit('close')">{{ s.install.close }}</button>
      </template>
      <template v-else>
        <button v-if="phase === 'failed'" class="btn" type="button" data-test="install-back" @click="back">{{ s.install.again }}</button>
        <router-link v-if="phase === 'done' && result" class="btn" :to="`/jails/${result.jail.metadata.name}`" @click="emit('close')">{{ s.install.openJail }}</router-link>
        <button class="btn btn-primary" type="button" data-test="install-close" @click="emit('close')">{{ s.install.close }}</button>
      </template>
    </template>
  </Modal>
</template>

<style>
.inst-form { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 0 12px; align-items: start; }
.inst-form .field { align-content: start; }
.inst-wide { grid-column: 1 / -1; }
.inst-limits { border: 1px solid var(--border); border-radius: var(--radius); padding: 8px 12px 0; margin: 0 0 4px; min-width: 0; }
.inst-limits legend { font-weight: 500; font-size: 13px; padding: 0 4px; }
.inst-limits .hint { color: var(--text-3); font-size: 12px; margin-bottom: 8px; }
.inst-limit-row { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 0 12px; }
.inst-plan h3 { margin-bottom: 4px; }
.inst-steps { list-style: none; margin: 6px 0 0; padding: 0; border: 1px solid var(--border); border-radius: var(--radius); background: var(--surface-2); max-height: 340px; overflow-y: auto; }
.inst-steps li { display: flex; gap: 10px; align-items: flex-start; padding: 6px 10px; border-bottom: 1px solid var(--border); }
.inst-steps li:last-child { border-bottom: 0; }
.inst-steps .icon { margin-top: 2px; color: var(--text-3); }
.inst-steps [data-state='done'] .icon { color: var(--ok); }
.inst-steps [data-state='running'] { background: var(--accent-soft); }
.inst-steps [data-state='running'] .icon { color: var(--accent); animation: inst-spin 1.2s linear infinite; }
.inst-steps [data-state='failed'] { background: var(--danger-bg); }
.inst-steps [data-state='failed'] .icon { color: var(--danger); }
.inst-n { color: var(--text-3); font-size: 12px; padding-top: 1px; }
.inst-name { font-weight: 500; display: block; }
.inst-text { display: block; color: var(--text-2); font-size: 12px; overflow-wrap: anywhere; }
.inst-status { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; margin: 0; }
.inst-addr { margin-right: 10px; }
.inst-open { margin-left: 6px; }
.inst-where dd > div { overflow-wrap: anywhere; }
.inst-notes p:last-child { margin: 0; }
.inst-notes { background: var(--surface-2); border: 1px solid var(--border); border-radius: var(--radius); padding: 8px 12px; }
@keyframes inst-spin { to { transform: rotate(360deg); } }
@media (max-width: 600px) { .inst-form, .inst-limit-row { grid-template-columns: minmax(0, 1fr); } }
</style>
