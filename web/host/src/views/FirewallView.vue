<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { s } from '@/i18n'
import { api } from '@/api/endpoints'
import { ApiError } from '@jailops/shared/api/client'
import { useLoad } from '@jailops/shared/composables/load'
import { runOp } from '@jailops/shared/composables/ops'
import { blankRule, detailToRuleErrors, splitList, validateRules, type RuleErrors } from '@/lib/firewall'
import Icon from '@jailops/shared/components/Icon.vue'
import LoadState from '@jailops/shared/components/LoadState.vue'
import ErrorPanel from '@jailops/shared/components/ErrorPanel.vue'
import type { FirewallPolicy, AddressSet, FirewallRule } from '@jailops/shared/api/types'

const l = useLoad(async () => {
  const [policies, sets, jails] = await Promise.all([api.firewall.policies(), api.firewall.addressSets(), api.jails.list().catch(() => [])])
  return { policies, sets, jails }
})

const tab = ref<'policies' | 'sets'>('policies')
const selected = ref('')
const draft = ref<FirewallPolicy | null>(null)
const original = ref('')
const etag = ref<string | undefined>()
const apiError = ref<ApiError | null>(null)
const fieldErrors = ref<RuleErrors>({})
const saving = ref(false)

const setSel = ref('')
const setDraft = ref<AddressSet | null>(null)
const setOriginal = ref('')
const setEtag = ref<string | undefined>()
const setText = ref('')
const setInc = ref('')
const setError = ref<ApiError | null>(null)

watch(() => l.data.value, (d) => {
  if (!d) return
  if (!selected.value && d.policies.length) void pick(d.policies[0].metadata.name)
  if (!setSel.value && d.sets.length) void pickSet(d.sets[0].metadata.name)
}, { immediate: true })

const dirty = computed(() => !!draft.value && JSON.stringify(draft.value) !== original.value)
const setDirty = computed(() => !!setDraft.value && JSON.stringify(withSetText()) !== setOriginal.value)
const attached = computed(() => (l.data.value?.jails ?? []).filter((j) => j.spec.firewall?.policy === selected.value).map((j) => j.metadata.name))

async function pick(n: string): Promise<void> {
  selected.value = n
  apiError.value = null
  fieldErrors.value = {}
  try {
    const r = await api.firewall.policy(n)
    r.data.spec.rules ??= []
    draft.value = structuredClone(r.data)
    original.value = JSON.stringify(r.data)
    etag.value = r.etag
  } catch (e) {
    apiError.value = e instanceof ApiError ? e : null
  }
}
function revert(): void {
  if (draft.value) draft.value = JSON.parse(original.value)
  fieldErrors.value = {}
  apiError.value = null
}
const rules = computed<FirewallRule[]>(() => draft.value?.spec.rules ?? [])
function add(): void {
  draft.value?.spec.rules?.push(blankRule())
}
function remove(i: number): void {
  draft.value?.spec.rules?.splice(i, 1)
}
function move(i: number, d: number): void {
  const r = draft.value?.spec.rules
  if (!r || i + d < 0 || i + d >= r.length) return
  const [x] = r.splice(i, 1)
  r.splice(i + d, 0, x)
}
function list(r: FirewallRule, k: 'from' | 'to' | 'ports', v: string): void {
  r[k] = splitList(v)
}
function toggleRedirect(r: FirewallRule, on: boolean): void {
  r.redirect = on ? { host_port: 8080, jail_port: 80 } : undefined
}
function clean(p: FirewallPolicy): FirewallPolicy {
  const c = structuredClone(p)
  c.spec.rules = (c.spec.rules ?? []).map((r) => {
    const x = { ...r }
    if (!x.name) delete x.name
    if (!x.proto) delete x.proto
    for (const k of ['from', 'to', 'ports'] as const) if (!x[k]?.length) delete x[k]
    if (!x.log) delete x.log
    if (!x.redirect) delete x.redirect
    return x
  })
  if (!c.spec.raw) delete c.spec.raw
  return c
}
async function save(): Promise<void> {
  if (!draft.value) return
  fieldErrors.value = validateRules(rules.value, s.firewall)
  apiError.value = null
  if (Object.keys(fieldErrors.value).length) return
  saving.value = true
  try {
    await runOp(s.firewall.savePolicyTitle(selected.value), () => api.firewall.putPolicy(clean(draft.value!), etag.value))
    await pick(selected.value)
  } catch (e) {
    apiError.value = e instanceof ApiError ? e : null
    fieldErrors.value = detailToRuleErrors(apiError.value?.detail)
  } finally {
    saving.value = false
  }
}

async function pickSet(n: string): Promise<void> {
  setSel.value = n
  setError.value = null
  try {
    const r = await api.firewall.addressSet(n)
    setDraft.value = structuredClone(r.data)
    setOriginal.value = JSON.stringify(r.data)
    setText.value = (r.data.spec.addresses ?? []).join('\n')
    setInc.value = (r.data.spec.includes ?? []).join(', ')
    setEtag.value = r.etag
  } catch (e) {
    setError.value = e instanceof ApiError ? e : null
  }
}
function withSetText(): AddressSet {
  const d = structuredClone(setDraft.value!)
  d.spec.addresses = splitList(setText.value)
  const inc = splitList(setInc.value)
  if (inc.length) d.spec.includes = inc
  else delete d.spec.includes
  return d
}
async function saveSet(): Promise<void> {
  if (!setDraft.value) return
  setError.value = null
  try {
    await runOp(s.firewall.saveSetTitle(setSel.value), () => api.firewall.putAddressSet(withSetText(), setEtag.value))
    await pickSet(setSel.value)
  } catch (e) {
    setError.value = e instanceof ApiError ? e : null
  }
}
const err = (i: number, f: string) => fieldErrors.value[`${i}.${f}`]
</script>

<template>
  <div>
    <div class="page-head"><h1>{{ s.firewall.title }}</h1></div>
    <div class="tabs" role="tablist">
      <button type="button" role="tab" :aria-selected="tab === 'policies'" @click="tab = 'policies'">{{ s.nav.firewall }}</button>
      <button type="button" role="tab" :aria-selected="tab === 'sets'" @click="tab = 'sets'">{{ s.firewall.sets }}</button>
    </div>
    <LoadState :loading="l.loading.value" :error="l.error.value" :has-data="!!l.data.value" @retry="l.reload">
      <div v-if="tab === 'policies'" class="fw-layout">
        <ul class="plain picker" aria-label="Policies">
          <li v-for="p in l.data.value?.policies" :key="p.metadata.name">
            <button type="button" class="btn btn-ghost mono" :class="{ sel: selected === p.metadata.name }" :aria-current="selected === p.metadata.name" @click="pick(p.metadata.name)">{{ p.metadata.name }}</button>
          </li>
          <li v-if="!l.data.value?.policies.length" class="muted">{{ s.firewall.empty }}</li>
        </ul>
        <section v-if="draft" class="panel" data-test="policy-editor">
          <header>
            <div class="row"><h2 class="mono">{{ draft.metadata.name }}</h2><span v-if="dirty" class="pill warn">{{ s.firewall.unsaved }}</span></div>
            <div class="row">
              <button class="btn" type="button" :disabled="!dirty" @click="revert">{{ s.firewall.revert }}</button>
              <button class="btn btn-primary" type="button" :disabled="!dirty || saving" data-test="save-policy" @click="save">{{ s.firewall.save }}</button>
            </div>
          </header>
          <div class="body stack">
            <ErrorPanel v-if="apiError" :error="apiError" />
            <p v-if="attached.length" class="muted">{{ s.firewall.usedBy }}: <template v-for="(n, i) in attached" :key="n"><span v-if="i">, </span><router-link :to="`/jails/${n}`" class="mono refl">{{ n }}</router-link></template></p>
            <div class="row">
              <div class="field" style="min-width: 160px"><label for="din">{{ s.firewall.defaultIn }}</label>
                <select id="din" v-model="draft.spec.default_in"><option value="">-</option><option>allow</option><option>deny</option></select></div>
              <div class="field" style="min-width: 160px"><label for="dout">{{ s.firewall.defaultOut }}</label>
                <select id="dout" v-model="draft.spec.default_out"><option value="">-</option><option>allow</option><option>deny</option></select></div>
            </div>
            <h3>{{ s.firewall.rules }}</h3>
            <p v-if="!rules.length" class="muted">{{ s.firewall.noRules }}</p>
            <ol class="rules">
              <li v-for="(r, i) in rules" :key="i" class="rule" :data-rule="i">
                <div class="rule-head row">
                  <span class="mono faint">{{ i + 1 }}</span>
                  <input v-model="r.name" type="text" :placeholder="s.firewall.ruleName" :aria-label="s.firewall.ruleName + ' ' + (i + 1)" style="max-width: 200px" />
                  <span class="grow"></span>
                  <button class="btn btn-sm btn-icon" type="button" :aria-label="s.firewall.moveUp" :disabled="i === 0" @click="move(i, -1)"><Icon name="arrowUp" /></button>
                  <button class="btn btn-sm btn-icon" type="button" :aria-label="s.firewall.moveDown" :disabled="i === rules.length - 1" @click="move(i, 1)"><Icon name="arrowDown" /></button>
                  <button class="btn btn-sm btn-icon btn-danger" type="button" :aria-label="s.firewall.removeRule" @click="remove(i)"><Icon name="trash" /></button>
                </div>
                <div class="rule-grid">
                  <div class="field"><label :for="`a${i}`">{{ s.firewall.action }}</label><select :id="`a${i}`" v-model="r.action"><option>allow</option><option>deny</option></select></div>
                  <div class="field"><label :for="`d${i}`">{{ s.firewall.direction }}</label><select :id="`d${i}`" v-model="r.direction"><option>in</option><option>out</option></select></div>
                  <div class="field"><label :for="`p${i}`">{{ s.firewall.proto }}</label><select :id="`p${i}`" v-model="r.proto"><option value="">any</option><option>tcp</option><option>udp</option><option>icmp</option><option>icmp6</option></select></div>
                  <div class="field"><label :for="`f${i}`">{{ s.firewall.from }}</label><input :id="`f${i}`" class="mono" type="text" :value="(r.from ?? []).join(', ')" :placeholder="s.firewall.any" @input="list(r, 'from', ($event.target as HTMLInputElement).value)" /></div>
                  <div class="field"><label :for="`t${i}`">{{ s.firewall.to }}</label><input :id="`t${i}`" class="mono" type="text" :value="(r.to ?? []).join(', ')" :placeholder="s.firewall.any" @input="list(r, 'to', ($event.target as HTMLInputElement).value)" /></div>
                  <div class="field">
                    <label :for="`o${i}`">{{ s.firewall.ports }}</label>
                    <input :id="`o${i}`" class="mono" type="text" :class="{ invalid: err(i, 'ports') }" :value="(r.ports ?? []).join(', ')" :aria-invalid="!!err(i, 'ports')" :aria-describedby="err(i, 'ports') ? `oe${i}` : undefined" @input="list(r, 'ports', ($event.target as HTMLInputElement).value)" />
                    <span v-if="err(i, 'ports')" :id="`oe${i}`" class="err" data-test="field-error">{{ err(i, 'ports') }}</span>
                  </div>
                </div>
                <span v-for="f in (['from', 'to', 'action', 'direction', 'proto'] as const)" :key="f"><span v-if="err(i, f)" class="err" data-test="field-error">{{ f }}: {{ err(i, f) }}</span></span>
                <div class="row">
                  <label class="check"><input v-model="r.log" type="checkbox" />{{ s.firewall.log }}</label>
                  <label class="check"><input type="checkbox" :checked="!!r.redirect" @change="toggleRedirect(r, ($event.target as HTMLInputElement).checked)" />{{ s.firewall.redirect }}</label>
                  <template v-if="r.redirect">
                    <input v-model.number="r.redirect.host_port" type="number" min="1" max="65535" :aria-label="s.firewall.hostPort" style="width: 90px" />
                    <span class="faint">to</span>
                    <input v-model.number="r.redirect.jail_port" type="number" min="1" max="65535" :aria-label="s.firewall.jailPort" style="width: 90px" />
                  </template>
                  <span v-if="err(i, 'redirect')" class="err" data-test="field-error">{{ err(i, 'redirect') }}</span>
                </div>
              </li>
            </ol>
            <div><button class="btn" type="button" @click="add"><Icon name="plus" />{{ s.firewall.addRule }}</button></div>
            <div class="field">
              <label for="raw">{{ s.firewall.raw }}</label>
              <textarea id="raw" v-model="draft.spec.raw" rows="4" spellcheck="false"></textarea>
            </div>
          </div>
        </section>
      </div>

      <div v-else class="fw-layout">
        <ul class="plain picker" aria-label="Address sets">
          <li v-for="a in l.data.value?.sets" :key="a.metadata.name"><button type="button" class="btn btn-ghost mono" :class="{ sel: setSel === a.metadata.name }" @click="pickSet(a.metadata.name)">@{{ a.metadata.name }}</button></li>
          <li v-if="!l.data.value?.sets.length" class="muted">{{ s.firewall.emptySets }}</li>
        </ul>
        <section v-if="setDraft" class="panel">
          <header>
            <h2 class="mono">@{{ setDraft.metadata.name }}</h2>
            <button class="btn btn-primary" type="button" :disabled="!setDirty" @click="saveSet">{{ s.firewall.save }}</button>
          </header>
          <div class="body stack">
            <ErrorPanel v-if="setError" :error="setError" />
            <div class="field"><label for="sa">{{ s.firewall.addresses }}</label><textarea id="sa" v-model="setText" rows="8" spellcheck="false"></textarea></div>
            <div class="field"><label for="si">{{ s.firewall.includes }}</label><input id="si" v-model="setInc" class="mono" type="text" /></div>
          </div>
        </section>
      </div>
    </LoadState>
  </div>
</template>

<style>
.fw-layout { display: grid; grid-template-columns: 220px minmax(0, 1fr); gap: 12px; align-items: start; }
.picker { background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius-lg); padding: 4px; }
.picker .btn { width: 100%; justify-content: flex-start; }
.picker .btn.sel { background: var(--accent-soft); font-weight: 600; }
.rules { list-style: none; padding: 0; margin: 0; display: grid; gap: 8px; }
.rule { border: 1px solid var(--border); border-radius: var(--radius); padding: 8px; background: var(--surface-2); }
.rule-head { margin-bottom: 8px; }
.rule-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(150px, 1fr)); gap: 0 8px; }
.rule .field { margin-bottom: 8px; }
.rule .err { display: block; }
@media (max-width: 800px) { .fw-layout { grid-template-columns: minmax(0, 1fr); } }
</style>
