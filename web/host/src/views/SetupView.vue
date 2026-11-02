<script setup lang="ts">
// The first-run wizard: shown while the host has no administrator. Step 1 creates the first one with the setup token
// the host printed on its console, and signs in; every later step uses the API like the rest of the console and can
// be skipped. The step state lives in this component, so a reload after the account ends the wizard (everything it
// does can be done later from the console).
import { computed, nextTick, onMounted, reactive, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { s } from '@/i18n'
import { api } from '@/api/endpoints'
import { ApiError } from '@jailops/shared/api/client'
import { runOp } from '@jailops/shared/composables/ops'
import { session, loginWithPassword } from '@jailops/shared/composables/session'
import Callout from '@jailops/shared/components/Callout.vue'
import CopyField from '@jailops/shared/components/CopyField.vue'
import ErrorPanel from '@jailops/shared/components/ErrorPanel.vue'
import CertificatePanel from '@/components/CertificatePanel.vue'
import { setPending } from '@/composables/pending'
import { refreshProjects } from '@/composables/project'
import { enableLines, RESTART, validDNSName, hostAddr } from '@/lib/host'
import { words } from '@/lib/hostfw'
import { STEPS, nextStep, skippable, passwordProblem, validUserName, cleanToken, setupFirewall, setupNetwork, v4Plan, SETUP_CONFIRM_WITHIN, type Step } from '@/lib/setup'
import { nameProblem } from '@jailops/shared/lib/names'
import { DEMO_SETUP_TOKEN } from '@/demo/setupToken'
import type { HostFirewall, HostIdentity, HostPendingChange } from '@jailops/shared/api/types'

const router = useRouter()
const step = ref<Step>('account')
const heading = ref<HTMLElement | null>(null)
const index = computed(() => STEPS.indexOf(step.value) + 1)
const busy = ref(false)
const error = ref<ApiError | null>(null)
const asError = (e: unknown) => (e instanceof ApiError ? e : new ApiError({ message: String(e) }))
const clientAddress = ref('')

// The wizard is for a host without an administrator: once one exists, the normal sign-in takes over.
onMounted(async () => {
  if (session.authed) return
  try {
    await api.setup.status()
  } catch (e) {
    if (e instanceof ApiError && e.code === 'not_found') void router.replace('/login')
  }
})

watch(step, async () => {
  error.value = null
  await nextTick()
  heading.value?.focus()
  void loadStep()
})
function next(): void { step.value = nextStep(step.value) }

// ---- 1: the account ----
const acc = reactive({ token: '', name: 'admin', password: '', repeat: '', touched: false })
const accProblems = computed(() => {
  const e: Record<string, string> = {}
  if (!cleanToken(acc.token)) e.token = s.setup.tokenMissing
  if (!validUserName(acc.name)) e.name = s.setup.nameRule
  const p = passwordProblem(acc.password, acc.name)
  if (p) e.password = s.setup.passwordRule(p)
  if (acc.repeat !== acc.password) e.repeat = s.setup.repeatRule
  return e
})
const accErr = (f: string) => (acc.touched ? accProblems.value[f] : undefined)
async function createAccount(): Promise<void> {
  acc.touched = true
  if (Object.keys(accProblems.value).length) return
  busy.value = true
  error.value = null
  try {
    const r = await api.setup.run({ token: cleanToken(acc.token), name: acc.name, password: acc.password })
    if (r.kind === 'sync') clientAddress.value = r.data.client_address ?? ''
    await loginWithPassword(acc.name, acc.password)
    acc.password = ''
    acc.repeat = ''
    next()
  } catch (e) {
    error.value = asError(e)
  } finally {
    busy.value = false
  }
}

// ---- 2: identity ----
const ident = ref<HostIdentity | null>(null)
const idf = reactive({ hostname: '', nameservers: '', ntp: '', ntpEnable: true })
const idProblems = computed(() => {
  const e: Record<string, string> = {}
  if (idf.hostname && !validDNSName(idf.hostname)) e.hostname = s.setup.hostnameRule
  const ns = words(idf.nameservers)
  if (!ns.length || ns.length > 3 || ns.some((x) => !hostAddr(x))) e.nameservers = s.setup.nameserversRule
  if (words(idf.ntp).length > 4) e.ntp = s.setup.ntpRule
  return e
})
const idDone = ref(false)
// The name the host was just given: the daemon keeps reporting the one it started with until it restarts, so the
// header, the certificate step and its client command use this one.
const newHost = ref('')
const hostName = computed(() => newHost.value || session.info?.hostname || '')
async function applyIdentity(): Promise<void> {
  if (Object.keys(idProblems.value).length) return
  busy.value = true
  error.value = null
  try {
    await runOp(s.host.applyTitle, () => api.host.apply({ spec: { ...(idf.hostname ? { hostname: idf.hostname } : {}), resolvers: { nameservers: words(idf.nameservers) }, ntp: { servers: words(idf.ntp), enable: idf.ntpEnable } } }))
    idDone.value = true
    if (idf.hostname && idf.hostname !== session.info?.hostname) newHost.value = idf.hostname
    next()
  } catch (e) {
    error.value = asError(e)
  } finally {
    busy.value = false
  }
}

// ---- 3: certificate ----
// Continue carries out the chosen way (it opens that dialog); Skip keeps the self-signed certificate and says so.
const certPanel = ref<InstanceType<typeof CertificatePanel> | null>(null)
const certWay = ref<'acme' | 'upload'>('acme')
const certChanged = ref('')
const certSkipped = ref(false)
function certContinue(): void {
  if (certChanged.value) return next()
  if (certWay.value === 'acme') certPanel.value?.openAcme()
  else certPanel.value?.openUpload()
}
function onCertChanged(): void {
  const src = certPanel.value?.source() ?? ''
  if (src && src !== 'self-signed') {
    certChanged.value = s.cert.sources[src] ?? src
    certSkipped.value = false
    next()
  }
}
function certSkip(): void {
  certSkipped.value = !certChanged.value
  next()
}

// ---- 4: firewall ----
const fw = ref<HostFirewall | null>(null)
const iKnow = ref(false)
const fwPending = ref<HostPendingChange | null>(null)
const fwConfirmed = ref(false)
async function applyFirewall(): Promise<void> {
  if (!clientAddress.value && !iKnow.value) return
  busy.value = true
  error.value = null
  try {
    const r = await runOp(s.setup.fwTitle, () => api.host.applyFirewall({ spec: setupFirewall(clientAddress.value), confirm_within: SETUP_CONFIRM_WITHIN, ...(clientAddress.value ? {} : { i_know: true }) }))
    fwPending.value = r.data?.pending ?? null
    if (fwPending.value) setPending({ pending: fwPending.value })
    else fwConfirmed.value = true
  } catch (e) {
    error.value = asError(e)
  } finally {
    busy.value = false
  }
}
async function confirmFirewall(): Promise<void> {
  busy.value = true
  error.value = null
  try {
    const r = await api.host.confirm(fwPending.value?.id)
    if (r.kind === 'sync') setPending(r.data)
    fwPending.value = null
    fwConfirmed.value = true
  } catch (e) {
    error.value = asError(e)
  } finally {
    busy.value = false
  }
}

// ---- 5: storage, project and network ----
const have = reactive({ loaded: false, storage: 0, projects: 0, networks: 0 })
// what this step makes, fixed when it loads: the fields stay on screen (disabled) while the objects are created
const make = reactive({ storage: false, project: false, network: false })
const creatingWhat = ref('')
const df = reactive({ project: 'main', network: 'lan', subnet: '10.77.0.0/24', uplink: '' })
const pool = computed(() => session.info?.environment.pool ?? '')
const dfProblems = computed(() => {
  const e: Record<string, string> = {}
  if (make.project && nameProblem(df.project, 'project')) e.project = nameProblem(df.project, 'project')
  if (make.network && nameProblem(df.network)) e.network = nameProblem(df.network)
  if (make.network && !setupNetwork(df.subnet, df.uplink)) e.subnet = s.setup.subnetRule
  if (df.uplink && !/^[a-z][a-z0-9_.]{0,14}$/.test(df.uplink)) e.uplink = s.setup.uplinkRule
  return e
})
const dfDone = ref<string[]>([])
const subnetPlan = computed(() => v4Plan(df.subnet))
async function createDefaults(): Promise<void> {
  if (Object.keys(dfProblems.value).length) return
  busy.value = true
  error.value = null
  try {
    let storage = ''
    if (!have.storage) {
      storage = 'default'
      creatingWhat.value = s.setup.storageTitle(storage)
      await runOp(s.setup.storageTitle(storage), () => api.storage.create({ metadata: { name: storage }, spec: { dataset: `${pool.value}/jailops` } }))
      dfDone.value.push(s.setup.madeStorage(storage, `${pool.value}/jailops`))
      have.storage = 1
    }
    let network = ''
    if (!have.networks) {
      network = df.network
      creatingWhat.value = s.setup.networkTitle(network)
      await runOp(s.setup.networkTitle(network), () => api.networks.create({ metadata: { name: network }, spec: setupNetwork(df.subnet, df.uplink)! }))
      dfDone.value.push(s.setup.madeNetwork(network, df.subnet))
      have.networks = 1
    }
    if (!have.projects) {
      const p = df.project
      creatingWhat.value = s.setup.projectTitle(p)
      await runOp(s.setup.projectTitle(p), () => api.projects.create({ metadata: { name: p }, spec: { ...(network ? { networks: [{ network }] } : {}), storage: [{ storage: storage || '*' }] } }))
      dfDone.value.push(s.setup.madeProject(p))
      have.projects = 1
      await refreshProjects()
    }
    next()
  } catch (e) {
    error.value = asError(e)
  } finally {
    busy.value = false
    creatingWhat.value = ''
  }
}

async function loadStep(): Promise<void> {
  try {
    if (step.value === 'identity') {
      ident.value = await api.host.identity()
      const sp = ident.value.spec
      Object.assign(idf, { hostname: sp.hostname ?? '', nameservers: (sp.resolvers?.nameservers ?? []).join(' '), ntp: (sp.ntp?.servers ?? []).join(' '), ntpEnable: sp.ntp?.enable ?? true })
    } else if (step.value === 'firewall') {
      fw.value = await api.host.firewall()
    } else if (step.value === 'defaults') {
      const [st, pr, nw] = await Promise.all([api.storage.list(), api.projects.list(), api.networks.list()])
      Object.assign(have, { loaded: true, storage: st.length, projects: pr.length, networks: nw.length })
      Object.assign(make, { storage: !st.length, project: !pr.length, network: !nw.length })
    }
  } catch (e) {
    error.value = asError(e)
  }
}
const enable = computed(() => (ident.value ? enableLines(ident.value) : {}))
const fwManaged = computed(() => !!fw.value?.status.managed)
const nothingToMake = computed(() => have.loaded && !make.storage && !make.project && !make.network)
</script>

<template>
  <main id="main" class="setup">
    <section class="panel setup-card" data-test="setup">
      <header><h1>{{ s.setup.title }}</h1><span class="faint mono" data-test="setup-host">{{ hostName }}</span></header>
      <div class="body stack">
        <ol class="setup-steps" :aria-label="s.setup.progress">
          <li v-for="(x, i) in STEPS" :key="x" :class="{ active: x === step, done: i + 1 < index }" :aria-current="x === step ? 'step' : undefined">{{ s.setup.steps[x] }}</li>
        </ol>
        <h2 ref="heading" tabindex="-1" data-test="setup-step">{{ s.setup.stepTitle(index, STEPS.length, s.setup.steps[step]) }}</h2>

        <!-- 1: the first administrator -->
        <form v-if="step === 'account'" novalidate data-test="setup-account" @submit.prevent="createAccount">
          <p class="muted">{{ s.setup.accountLead }}</p>
          <Callout v-if="session.demo" tone="info" data-test="setup-demo-token">{{ s.setup.demoToken(DEMO_SETUP_TOKEN) }}</Callout>
          <div class="field"><label for="su-token">{{ s.setup.token }}</label>
            <input id="su-token" v-model="acc.token" class="mono" type="text" autocomplete="off" spellcheck="false" :aria-invalid="!!accErr('token')" data-test="setup-token" />
            <span v-if="accErr('token')" class="err" data-test="field-error">{{ accErr('token') }}</span><span v-else class="hint">{{ s.setup.tokenHint }}</span></div>
          <div class="field"><label for="su-name">{{ s.setup.name }}</label>
            <input id="su-name" v-model="acc.name" class="mono" type="text" autocomplete="username" spellcheck="false" :aria-invalid="!!accErr('name')" data-test="setup-name" />
            <span v-if="accErr('name')" class="err" data-test="field-error">{{ accErr('name') }}</span></div>
          <div class="field"><label for="su-pw">{{ s.setup.password }}</label>
            <input id="su-pw" v-model="acc.password" type="password" autocomplete="new-password" :aria-invalid="!!accErr('password')" data-test="setup-password" />
            <span v-if="accErr('password')" class="err" data-test="field-error">{{ accErr('password') }}</span><span v-else class="hint">{{ s.setup.passwordHint }}</span></div>
          <div class="field"><label for="su-pw2">{{ s.setup.repeat }}</label>
            <input id="su-pw2" v-model="acc.repeat" type="password" autocomplete="new-password" :aria-invalid="!!accErr('repeat')" data-test="setup-repeat" />
            <span v-if="accErr('repeat')" class="err" data-test="field-error">{{ accErr('repeat') }}</span></div>
          <ErrorPanel v-if="error" :error="error" />
          <div class="row"><button class="btn btn-primary" type="submit" :disabled="busy" data-test="setup-create">{{ s.setup.create }}</button></div>
        </form>

        <!-- 2: host name, resolvers, time -->
        <form v-else-if="step === 'identity'" novalidate @submit.prevent="applyIdentity">
          <p class="muted">{{ s.setup.identityLead }}</p>
          <template v-if="ident && !ident.status.managed">
            <Callout tone="info" data-test="setup-not-managed">{{ s.setup.notManaged }}</Callout>
            <div v-if="enable.config" class="field"><span class="label">{{ s.host.enableConfig }}</span><pre class="mono setup-pre">{{ enable.config }}</pre></div>
            <div v-if="enable.helper" class="field"><span class="label">{{ s.host.enableHelper }}</span><CopyField :value="enable.helper" label="priv-line" /></div>
            <div class="field"><span class="label">{{ s.host.enableRestart }}</span><CopyField :value="RESTART" label="restart" /></div>
          </template>
          <fieldset v-else-if="ident" class="plain-set">
            <div class="field"><label for="su-host">{{ s.host.hostname }}</label><input id="su-host" v-model="idf.hostname" class="mono" type="text" autocomplete="off" :aria-invalid="!!idProblems.hostname" data-test="setup-hostname" />
              <span v-if="idProblems.hostname" class="err">{{ idProblems.hostname }}</span><span v-else class="hint">{{ s.host.hostnameHint }}</span></div>
            <div class="field"><label for="su-ns">{{ s.host.nameservers }}</label><input id="su-ns" v-model="idf.nameservers" class="mono" type="text" autocomplete="off" :aria-invalid="!!idProblems.nameservers" data-test="setup-ns" />
              <span v-if="idProblems.nameservers" class="err">{{ idProblems.nameservers }}</span><span v-else class="hint">{{ s.setup.listHint }}</span></div>
            <div class="field"><label for="su-ntp">{{ s.host.ntpServers }}</label><input id="su-ntp" v-model="idf.ntp" class="mono" type="text" autocomplete="off" :aria-invalid="!!idProblems.ntp" data-test="setup-ntp" />
              <span v-if="idProblems.ntp" class="err">{{ idProblems.ntp }}</span><span v-else class="hint">{{ s.setup.listHint }}</span></div>
            <label class="check"><input v-model="idf.ntpEnable" type="checkbox" />{{ s.host.ntpEnable }}</label>
          </fieldset>
          <ErrorPanel v-if="error" :error="error" />
          <div class="row setup-nav">
            <button v-if="ident?.status.managed" class="btn btn-primary" type="submit" :disabled="busy || Object.keys(idProblems).length > 0" data-test="setup-apply">{{ s.setup.applyNext }}</button>
            <button class="btn btn-ghost" type="button" data-test="setup-skip" @click="next">{{ s.setup.skip }}</button>
          </div>
        </form>

        <!-- 3: the certificate -->
        <div v-else-if="step === 'certificate'" class="stack">
          <Callout v-if="newHost" tone="ok" data-test="setup-renamed">{{ s.setup.renamed(newHost) }}</Callout>
          <p class="muted">{{ s.setup.certLead }}</p>
          <fieldset class="field plain-set" data-test="setup-cert-way">
            <legend class="label">{{ s.setup.certChoice }}</legend>
            <label class="check"><input v-model="certWay" type="radio" value="acme" name="su-cert" data-test="setup-cert-acme" />{{ s.setup.certAcme }}</label>
            <label class="check"><input v-model="certWay" type="radio" value="upload" name="su-cert" data-test="setup-cert-upload" />{{ s.setup.certUpload }}</label>
          </fieldset>
          <CertificatePanel ref="certPanel" :hostname="hostName" @changed="onCertChanged" />
          <div class="row setup-nav">
            <button class="btn btn-primary" type="button" data-test="setup-next" @click="certContinue">{{ certChanged ? s.setup.continue : certWay === 'acme' ? s.setup.continueAcme : s.setup.continueUpload }}</button>
            <button class="btn btn-ghost" type="button" data-test="setup-skip" @click="certSkip">{{ s.setup.skipCert }}</button>
          </div>
        </div>

        <!-- 4: the host firewall -->
        <div v-else-if="step === 'firewall'" class="stack">
          <Callout v-if="certSkipped" tone="warn" data-test="setup-cert-skipped">{{ s.setup.certSkipped }}</Callout>
          <Callout v-else-if="certChanged" tone="ok" data-test="setup-cert-now">{{ s.setup.certNow(certChanged) }}</Callout>
          <p class="muted">{{ s.setup.fwLead }}</p>
          <Callout v-if="fw && !fwManaged" tone="info" data-test="setup-fw-not-managed">{{ s.setup.fwNotManaged }}</Callout>
          <template v-else-if="fw">
            <ul class="setup-list" data-test="setup-fw-plan">
              <li>{{ clientAddress ? s.setup.fwSsh(clientAddress) : s.setup.fwSshAny }}</li>
              <li>{{ s.setup.fwConsole }}</li>
              <li>{{ s.setup.fwRest }}</li>
            </ul>
            <Callout tone="info">{{ s.setup.fwRevert(SETUP_CONFIRM_WITHIN) }}</Callout>
            <template v-if="!clientAddress">
              <Callout tone="warn">{{ s.setup.fwUnknown }}</Callout>
              <label class="check"><input v-model="iKnow" type="checkbox" data-test="setup-fw-iknow" />{{ s.setup.fwIKnow }}</label>
            </template>
            <Callout v-if="fwPending" tone="warn" data-test="setup-fw-pending">{{ s.setup.fwPending }}</Callout>
            <Callout v-if="fwConfirmed" tone="ok" data-test="setup-fw-done">{{ s.setup.fwDone }}</Callout>
          </template>
          <ErrorPanel v-if="error" :error="error" />
          <div class="row setup-nav">
            <button v-if="fwManaged && !fwPending && !fwConfirmed" class="btn btn-primary" type="button" :disabled="busy || (!clientAddress && !iKnow)" data-test="setup-fw-apply" @click="applyFirewall">{{ s.setup.fwApply }}</button>
            <button v-if="fwPending" class="btn btn-primary" type="button" :disabled="busy" data-test="setup-fw-confirm" @click="confirmFirewall">{{ s.setup.fwConfirm }}</button>
            <button v-if="fwConfirmed" class="btn btn-primary" type="button" data-test="setup-next" @click="next">{{ s.setup.continue }}</button>
            <button v-if="!fwConfirmed" class="btn btn-ghost" type="button" data-test="setup-skip" @click="next">{{ s.setup.skip }}</button>
          </div>
        </div>

        <!-- 5: storage, a project and a network -->
        <form v-else-if="step === 'defaults'" novalidate @submit.prevent="createDefaults">
          <p class="muted">{{ s.setup.defaultsLead }}</p>
          <Callout v-if="nothingToMake" tone="ok" data-test="setup-defaults-exist">{{ s.setup.defaultsExist }}</Callout>
          <fieldset v-else-if="have.loaded" class="plain-set" :disabled="busy">
            <p v-if="make.storage" data-test="setup-make-storage">{{ s.setup.makeStorage(`${pool}/jailops`) }}</p>
            <div v-if="make.project" class="field"><label for="su-proj">{{ s.setup.projectName }}</label><input id="su-proj" v-model="df.project" class="mono" type="text" autocomplete="off" :aria-invalid="!!dfProblems.project" data-test="setup-project" />
              <span v-if="dfProblems.project" class="err">{{ dfProblems.project }}</span></div>
            <template v-if="make.network">
              <div class="field"><label for="su-net">{{ s.setup.networkName }}</label><input id="su-net" v-model="df.network" class="mono" type="text" autocomplete="off" :aria-invalid="!!dfProblems.network" data-test="setup-network" />
                <span v-if="dfProblems.network" class="err">{{ dfProblems.network }}</span></div>
              <div class="field"><label for="su-sub">{{ s.setup.subnet }}</label><input id="su-sub" v-model="df.subnet" class="mono" type="text" autocomplete="off" :aria-invalid="!!dfProblems.subnet" aria-describedby="su-sub-h" data-test="setup-subnet" />
                <span v-if="dfProblems.subnet" id="su-sub-h" class="err">{{ dfProblems.subnet }}</span><span v-else id="su-sub-h" class="hint" data-test="setup-subnet-plan">{{ s.setup.subnetHint }} {{ subnetPlan ? s.setup.subnetPlan(subnetPlan.gateway, subnetPlan.range) : '' }}</span></div>
              <div class="field"><label for="su-up">{{ s.setup.uplink }}</label><input id="su-up" v-model="df.uplink" class="mono" type="text" autocomplete="off" placeholder="em0" :aria-invalid="!!dfProblems.uplink" data-test="setup-uplink" />
                <span v-if="dfProblems.uplink" class="err">{{ dfProblems.uplink }}</span><span v-else class="hint">{{ s.setup.uplinkHint }}</span></div>
            </template>
            <ul v-if="busy || dfDone.length" class="setup-list" role="status" data-test="setup-progress"><li v-for="d in dfDone" :key="d">{{ d }}</li><li v-if="creatingWhat">{{ s.setup.creating(creatingWhat) }}</li></ul>
          </fieldset>
          <ErrorPanel v-if="error" :error="error" />
          <div class="row setup-nav">
            <button v-if="have.loaded && !nothingToMake" class="btn btn-primary" type="submit" :disabled="busy || Object.keys(dfProblems).length > 0" data-test="setup-make">{{ s.setup.make }}</button>
            <button v-if="nothingToMake" class="btn btn-primary" type="button" data-test="setup-next" @click="next">{{ s.setup.continue }}</button>
            <button v-else class="btn btn-ghost" type="button" data-test="setup-skip" @click="next">{{ s.setup.skip }}</button>
          </div>
        </form>

        <!-- 6: done -->
        <div v-else class="stack" data-test="setup-done">
          <Callout tone="ok">{{ s.setup.doneLead }}</Callout>
          <ul v-if="dfDone.length" class="setup-list"><li v-for="d in dfDone" :key="d">{{ d }}</li></ul>
          <div class="row">
            <router-link class="btn btn-primary" to="/apps" data-test="setup-apps">{{ s.setup.toApps }}</router-link>
            <router-link class="btn" to="/" data-test="setup-overview">{{ s.setup.toOverview }}</router-link>
          </div>
        </div>
        <p v-if="skippable(step)" class="faint">{{ s.setup.later }}</p>
      </div>
    </section>
  </main>
</template>

<style scoped>
.setup { min-height: 100vh; display: grid; place-items: start center; padding: var(--gutter); }
.setup-card { width: min(720px, 100%); margin-top: 24px; }
.setup-card > header { justify-content: space-between; flex-wrap: wrap; }
.setup-steps { display: flex; flex-wrap: wrap; gap: 4px 12px; margin: 0; padding: 0; list-style: none; font-size: 12px; color: var(--text-3); }
.setup-steps li.active { color: var(--text); font-weight: 600; }
.setup-steps li.done { color: var(--ok); }
h2 { font-size: 16px; margin: 0; }
h2:focus { outline: none; }
.setup-list { margin: 0; padding-left: 20px; }
.setup-list li { margin: 2px 0; overflow-wrap: anywhere; }
.setup-pre { margin: 0; padding: 8px 12px; background: var(--surface-2); border: 1px solid var(--border); border-radius: var(--radius); white-space: pre; overflow-x: auto; }
.plain-set { border: 0; margin: 0; padding: 0; min-width: 0; }
.setup-nav { margin-top: 12px; }
</style>
