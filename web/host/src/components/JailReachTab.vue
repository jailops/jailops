<script setup lang="ts">
import { computed, ref } from 'vue'
import { s } from '@/i18n'
import { api } from '@/api/endpoints'
import { useLoad } from '@jailops/shared/composables/load'
import { session } from '@jailops/shared/composables/session'
import LoadState from '@jailops/shared/components/LoadState.vue'
import Callout from '@jailops/shared/components/Callout.vue'
import ConfirmDialog from '@jailops/shared/components/ConfirmDialog.vue'
import Icon from '@jailops/shared/components/Icon.vue'
import ExposeDialog from '@/components/ExposeDialog.vue'
import JailPolicies from '@/components/JailPolicies.vue'
import type { ExposeEntry, ExposeResult, Jail, ReachRow } from '@jailops/shared/api/types'

// What reaches a jail and what it reaches, read from the anchor the daemon loads for it (GET /jails/NAME/reach).
const props = defineProps<{ jail: Jail }>()
const l = useLoad(() => api.reach.jail(props.jail.metadata.name))
const r = computed(() => l.data.value)
// The ports exposed with jailops expose are the jail's own spec: read it again after every change.
const cur = useLoad(() => api.jails.get(props.jail.metadata.name))
const current = computed(() => cur.data.value ?? props.jail)
const entries = computed(() => current.value.spec.expose ?? [])
// whether the kernel has what the policies say: the FirewallApplied condition (absent while the jail is stopped or has no firewall)
const applied = computed(() => current.value.status.conditions?.find((c) => c.type === 'FirewallApplied'))
const reload = (): void => { l.reload(); cur.reload() }

// The server decides; this only avoids offering what it would refuse.
const block = computed(() => {
  const role = session.role
  if (role && role !== 'operator' && role !== 'admin') return s.expose.readOnly(role)
  return ''
})
const exposeOpen = ref(false)
const removing = ref<ExposeEntry | null>(null)
const portsOf = (e: ExposeEntry): string => `${e.ports.join(',')}/${e.proto || 'tcp'}`
const famOf = (e: ExposeEntry): string => (e.families?.length ? e.families.map((x) => 'IPv' + x).join(' + ') : 'IPv4 + IPv6')
const limitOf = (e: ExposeEntry): string => (e.from?.length ? e.from.join(', ') : s.reach.any)
const notes = ref<string[]>([])
const runRemove = async () => {
  const r = await api.reach.unexpose(props.jail.metadata.name, { proto: removing.value!.proto || 'tcp', ports: removing.value!.ports })
  notes.value = r.kind === 'sync' ? ((r.data as ExposeResult).notes ?? []) : []
  return r
}

const source = (x: ReachRow): string => (x.source === 'tunnel' && x.source_name ? `${s.reach.sources.tunnel} ${x.source_name}` : (s.reach.sources[x.source as keyof typeof s.reach.sources] ?? x.source))
const verdict = (x: ReachRow): string => (x.decision === 'pass' ? s.reach.open : x.decision === 'forward' ? s.reach.forward(`${x.host_address || s.reach.host}:${x.host_port}`) : s.reach.closed)
const tone = (x: ReachRow): string => (x.decision === 'none' ? 'ok' : x.source === 'internet' ? 'warn' : 'info')
const from = (x: ReachRow): string => (x.restricted && x.from?.length ? x.from.join(', ') : s.reach.any)
</script>

<template>
  <section class="panel" aria-labelledby="reach-h" data-test="reach-tab">
    <header>
      <h2 id="reach-h">{{ s.reach.title }}</h2>
      <span :title="block || undefined">
        <button class="btn btn-sm btn-primary" type="button" :disabled="!!block" data-test="expose-open" @click="exposeOpen = true"><Icon name="plus" />{{ s.expose.button }}</button>
      </span>
    </header>
    <div class="body stack">
      <Callout v-if="applied" :tone="applied.status === 'True' ? 'ok' : 'warn'" data-test="firewall-applied" :data-status="applied.status">
        <strong>{{ s.reach.applied }}</strong>
        {{ applied.status === 'True' ? s.reach.appliedYes(applied.message ?? '') : applied.status === 'Unknown' ? s.reach.appliedUnknown(applied.reason ?? '', applied.message ?? '') : s.reach.appliedNo(applied.reason ?? '', applied.message ?? '') }}
      </Callout>
      <JailPolicies v-if="r && !r.error" :jail="jail.metadata.name" :policies="r.policies ?? []" @changed="reload" />
      <p class="muted">{{ s.reach.lead }}</p>
      <Callout v-if="block" tone="info" data-test="expose-blocked">{{ block }}</Callout>
      <Callout v-for="n in notes" :key="n" tone="info" data-test="unexpose-note">{{ n }}</Callout>
      <div class="stack" data-test="expose-current">
        <h3>{{ s.expose.current }}</h3>
        <p v-if="!entries.length" class="empty" data-test="expose-none">{{ s.expose.currentNone }}</p>
        <ul v-else class="acc-grants">
          <li v-for="e in entries" :key="portsOf(e)" class="acc-grant" :data-expose="portsOf(e)">
            <div class="acc-head">
              <div class="acc-what"><span class="mono acc-src">{{ portsOf(e) }}</span><span class="badge info">{{ famOf(e) }}</span><span class="muted">{{ limitOf(e) }}</span></div>
              <button class="btn btn-sm" type="button" :disabled="!!block" :data-test="`unexpose-${portsOf(e)}`" @click="removing = e"><Icon name="trash" />{{ s.expose.unexpose }}</button>
            </div>
            <p v-if="e.host_port" class="muted mono">{{ s.reach.forward('host:' + e.host_port) }}</p>
            <p v-if="e.comment" class="muted">{{ e.comment }}</p>
          </li>
        </ul>
      </div>
      <LoadState :loading="l.loading.value" :error="l.error.value" :has-data="!!r" @retry="l.reload">
        <div v-if="r" class="stack">
          <Callout v-if="r.error" tone="warn">{{ r.error }}</Callout>
          <div v-else class="table-wrap">
            <table class="rtable" data-test="reach-rows">
              <thead><tr><th>{{ s.reach.from }}</th><th>{{ s.reach.family }}</th><th>{{ s.reach.proto }}</th><th>{{ s.reach.ports }}</th><th>{{ s.reach.limited }}</th><th>{{ s.reach.reach }}</th><th>{{ s.reach.by }}</th></tr></thead>
              <tbody>
                <tr v-for="(x, i) in r.rows" :key="i" :data-source="x.source" :data-decision="x.decision">
                  <td :data-label="s.reach.from">{{ source(x) }}</td>
                  <td :data-label="s.reach.family">{{ x.family ? 'IPv' + x.family.replace(',', ' + IPv') : '-' }}</td>
                  <td :data-label="s.reach.proto" class="mono">{{ x.proto || '-' }}</td>
                  <td :data-label="s.reach.ports" class="mono">{{ x.ports || '-' }}</td>
                  <td :data-label="s.reach.limited" class="mono">{{ x.decision === 'none' ? '-' : from(x) }}</td>
                  <td :data-label="s.reach.reach"><span class="pill" :class="tone(x)">{{ verdict(x) }}</span></td>
                  <td :data-label="s.reach.by" class="muted">{{ x.by }}</td>
                </tr>
              </tbody>
            </table>
          </div>
          <dl class="kv" data-test="reach-defaults">
            <dt>{{ s.reach.defaultIn }}</dt><dd>{{ s.reach.defaultText(r.default_in, r.default_in_by ?? []) }}</dd>
            <dt>{{ s.reach.defaultOut }}</dt><dd>{{ s.reach.defaultText(r.default_out, r.default_out_by ?? []) }}</dd>
          </dl>
          <h3>{{ s.reach.out }}</h3>
          <dl class="kv" data-test="reach-out">
            <dt>{{ s.reach.sources.internet }}</dt><dd>{{ r.out.internet || '-' }}</dd>
            <dt>{{ s.reach.sources.host }}</dt><dd>{{ r.out.host || '-' }}</dd>
            <dt>{{ s.reach.sources.jails }}</dt><dd>{{ r.out.jails || '-' }}</dd>
            <dt>{{ s.reach.sources['other-projects'] }}</dt><dd>{{ r.out.other_projects || '-' }}</dd>
          </dl>
          <details v-if="r.rules?.length" class="acc-rules">
            <summary>{{ s.reach.rules(r.rules.length) }}</summary>
            <div class="table-wrap">
              <table class="rtable">
                <thead><tr><th>#</th><th>{{ s.reach.where }}</th><th>{{ s.reach.dir }}</th><th>{{ s.reach.family }}</th><th>{{ s.reach.srcCol }}</th><th>{{ s.reach.dst }}</th><th>{{ s.reach.proto }}</th><th>{{ s.reach.ports }}</th><th>{{ s.reach.decision }}</th><th>{{ s.reach.by }}</th></tr></thead>
                <tbody>
                  <tr v-for="(x, i) in r.rules" :key="i" :title="x.pf">
                    <td>{{ i + 1 }}</td><td>{{ x.where }}</td><td>{{ x.direction }}</td><td>{{ x.family || '-' }}</td><td class="mono">{{ x.from || '-' }}</td><td class="mono">{{ x.to || '-' }}</td>
                    <td class="mono">{{ x.proto || '-' }}</td><td class="mono">{{ x.ports || '-' }}</td><td>{{ x.decision }}</td><td class="muted">{{ x.by }}</td>
                  </tr>
                </tbody>
              </table>
            </div>
          </details>
          <Callout v-for="n in r.notes ?? []" :key="n" tone="info">{{ n }}</Callout>
        </div>
      </LoadState>
    </div>
    <ExposeDialog :open="exposeOpen" :jail="current" @close="exposeOpen = false" @done="reload" />
    <ConfirmDialog :open="!!removing" :title="removing ? s.expose.unexposeTitle(portsOf(removing)) : ''" :confirm-label="s.expose.unexpose" danger :run="runRemove" @close="removing = null" @done="reload">
      <p v-if="removing" data-test="unexpose-dialog">{{ s.expose.unexposeText(portsOf(removing), jail.metadata.name) }}</p>
    </ConfirmDialog>
  </section>
</template>
