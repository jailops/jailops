<script setup lang="ts">
import { computed, ref } from 'vue'
import { s } from '@/i18n'
import { api } from '@/api/endpoints'
import { useLoad } from '@jailops/shared/composables/load'
import { can, hasExt } from '@jailops/shared/composables/session'
import { runOp } from '@jailops/shared/composables/ops'
import { bytes, stamp, ago } from '@jailops/shared/lib/format'
import Icon from '@jailops/shared/components/Icon.vue'
import LoadState from '@jailops/shared/components/LoadState.vue'
import Callout from '@jailops/shared/components/Callout.vue'
import ConfirmDialog from '@jailops/shared/components/ConfirmDialog.vue'
import BackupKeyPanel from '@/components/BackupKeyPanel.vue'
import RestoreWizard from '@/components/RestoreWizard.vue'
import type { Backup } from '@jailops/shared/api/types'

const l = useLoad(async () => {
  const [targets, policies, backups] = await Promise.all([api.backups.targets(), api.backups.policies(), api.backups.list()])
  return { targets, policies, backups }
})
const tab = ref<'snapshots' | 'runs' | 'targets' | 'policies' | 'key'>('snapshots')
const restoring = ref<Backup | null>(null)
const deleting = ref<Backup | null>(null)

const byJail = computed(() => {
  const m = new Map<string, Backup[]>()
  for (const b of l.data.value?.backups ?? []) {
    if (!m.has(b.spec.jail)) m.set(b.spec.jail, [])
    m.get(b.spec.jail)!.push(b)
  }
  for (const v of m.values()) v.sort((a, b) => (b.metadata.created_at ?? '').localeCompare(a.metadata.created_at ?? ''))
  return [...m.entries()].sort((a, b) => a[0].localeCompare(b[0]))
})
const runs = computed(() => byJail.value.map(([jail, list]) => ({ jail, last: list[0] })))

const requestDelete = () => runOp(s.backups.deleteTitle(deleting.value!.spec.snapshot), () => api.backups.requestDeletion(deleting.value!.metadata.name))
const depth = ref<'deep' | 'extract' | 'full'>('deep')
const verify = (b: Backup) => runOp(s.backups.verifyTitle(b.spec.snapshot), () => api.backups.verify(b.metadata.name, { depth: depth.value })).catch(() => undefined)
const run = (n: string) => runOp(s.backups.runTitle(n), () => api.backups.run({ policy: n })).catch(() => undefined)
const retention = (r: object) => Object.entries(r as Record<string, number | undefined>).filter(([, v]) => v).map(([k, v]) => `${v} ${k}`).join(', ') || '-'
const sel = (m: Record<string, string> | undefined) => Object.entries(m ?? {}).map(([k, v]) => `${k}=${v}`).join(', ') || '-'
</script>

<template>
  <div>
    <div class="page-head"><h1>{{ s.backups.title }}</h1></div>
    <Callout style="margin-bottom: 12px">{{ s.backups.keyless }}</Callout>
    <div class="tabs" role="tablist">
      <button v-for="[k, label] in ([['snapshots', s.backups.snapshots], ['runs', s.backups.lastRuns], ['targets', s.backups.targets], ['policies', s.backups.policies], ...(hasExt('backup_key') ? [['key', s.backups.keyTab]] : [])] as Array<['snapshots' | 'runs' | 'targets' | 'policies' | 'key', string]>)" :key="k" type="button" role="tab" :aria-selected="tab === k" @click="tab = k">{{ label }}</button>
    </div>
    <LoadState :loading="l.loading.value" :error="l.error.value" :has-data="!!l.data.value" @retry="l.reload">
      <div v-if="tab === 'snapshots'" class="stack">
        <p v-if="!byJail.length" class="empty">{{ s.backups.noBackups }}</p>
        <section v-for="[jail, list] in byJail" :key="jail" class="panel" :data-jail="jail">
          <header><h2 class="mono">{{ jail }}</h2><span class="faint">{{ list.length }}</span></header>
          <div class="table-wrap">
            <table>
              <thead><tr><th>{{ s.backups.snapshot }}</th><th class="num">{{ s.backups.size }}</th><th class="num hide-sm">{{ s.backups.newBytes }}</th><th>{{ s.backups.state }}</th><th><span class="sr-only">Actions</span></th></tr></thead>
              <tbody>
                <tr v-for="b in list" :key="b.metadata.name" :data-backup="b.metadata.name" :class="{ drift: b.status.failed }">
                  <td><span class="mono">{{ b.spec.snapshot }}</span><br /><span class="faint">{{ stamp(b.metadata.created_at) }} · {{ b.spec.full ? s.backups.full : s.backups.incremental }}</span></td>
                  <td class="num mono">{{ bytes(b.status.bytes) }}</td>
                  <td class="num mono hide-sm">{{ bytes(b.status.new_bytes) }}</td>
                  <td>
                    <span v-if="b.status.failed" class="pill danger" data-test="failed">{{ s.backups.failed }}</span>
                    <span v-else-if="b.status.verified" class="pill ok">{{ s.backups.verified }}</span>
                    <span v-else class="pill warn">{{ s.backups.unverified }}</span>
                    <span v-if="b.status.protected" class="pill info" data-test="protected" style="margin-left: 4px"><Icon name="lock" />{{ s.backups.protected }}</span>
                    <span v-if="b.status.pending_deletion" class="pill danger" data-test="pending" style="margin-left: 4px">{{ s.backups.pendingDeletion }}</span>
                    <div v-if="b.status.pending_deletion" class="faint">{{ s.backups.pendingUntil(stamp(b.status.pending_deletion)) }}</div>
                  </td>
                  <td>
                    <div class="row-actions">
                    <button v-if="can('restore')" class="btn btn-sm" type="button" :disabled="b.status.failed" @click="restoring = b">{{ s.backups.restore }}</button>
                    <select v-if="hasExt('backup_verify')" v-model="depth" class="depth" :aria-label="s.backups.depth"><option value="deep">deep</option><option value="extract">extract</option><option value="full">full</option></select><button v-if="hasExt('backup_verify')" class="btn btn-sm" type="button" @click="verify(b)">{{ s.backups.verify }}</button>
                    <button v-if="!b.status.pending_deletion" class="btn btn-sm btn-danger" type="button" :disabled="b.status.protected" @click="deleting = b">{{ s.backups.requestDelete }}</button>
                    </div>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </section>
      </div>

      <div v-else-if="tab === 'runs'" class="panel">
        <header><span class="muted">{{ s.backups.lastRunsHint }}</span></header>
        <p v-if="!runs.length" class="empty">{{ s.backups.noBackups }}</p>
        <div v-else class="table-wrap"><table>
          <thead><tr><th>{{ s.nav.jails }}</th><th>{{ s.backups.policy }}</th><th>{{ s.backups.lastBackup }}</th><th>{{ s.backups.state }}</th></tr></thead>
          <tbody><tr v-for="r in runs" :key="r.jail">
            <td class="mono">{{ r.jail }}</td><td class="mono">{{ r.last.spec.policy || '-' }}</td><td>{{ ago(r.last.metadata.created_at) }} <span class="faint">{{ bytes(r.last.status.new_bytes) }}</span></td>
            <td><span class="pill" :class="r.last.status.failed ? 'danger' : r.last.status.verified ? 'ok' : 'warn'">{{ r.last.status.failed ? s.backups.failed : r.last.status.verified ? s.backups.verified : s.backups.unverified }}</span></td>
          </tr></tbody></table></div>
      </div>

      <BackupKeyPanel v-else-if="tab === 'key'" />

      <div v-else-if="tab === 'targets'" class="panel">
        <p v-if="!l.data.value?.targets.length" class="empty">{{ s.backups.noTargets }}</p>
        <div v-else class="table-wrap"><table>
          <thead><tr><th>{{ s.jails.name }}</th><th>{{ s.backups.kind }}</th><th>{{ s.backups.address }}</th><th>{{ s.backups.fingerprint }}</th></tr></thead>
          <tbody><tr v-for="t in l.data.value?.targets" :key="t.metadata.name">
            <td class="mono">{{ t.metadata.name }}</td><td>{{ t.spec.kind }}</td><td class="mono">{{ t.spec.address || t.spec.path || '-' }}</td><td class="mono faint">{{ t.spec.fingerprint?.slice(0, 23) || '-' }}</td>
          </tr></tbody></table></div>
      </div>

      <div v-else class="panel">
        <p v-if="!l.data.value?.policies.length" class="empty">{{ s.backups.noPolicies }}</p>
        <div v-else class="table-wrap"><table>
          <thead><tr><th>{{ s.jails.name }}</th><th>{{ s.backups.selector }}</th><th>{{ s.backups.targets }}</th><th>{{ s.backups.schedule }}</th><th>{{ s.backups.retention }}</th><th class="hide-sm">{{ s.backups.verifySchedule }}</th><th></th></tr></thead>
          <tbody><tr v-for="p in l.data.value?.policies" :key="p.metadata.name">
            <td class="mono">{{ p.metadata.name }}</td>
            <td class="mono">{{ sel(p.spec.selector) }}</td><td class="mono">{{ (p.spec.targets ?? []).join(', ') }}</td><td class="mono">{{ p.spec.schedule }}</td>
            <td>{{ retention(p.spec.retention) }}</td>
            <td class="mono hide-sm">{{ p.spec.verify_schedule || '-' }}<span v-if="p.spec.verify_mode" class="faint"> ({{ p.spec.verify_mode }})</span></td>
            <td><button v-if="hasExt('backup_run')" class="btn btn-sm" type="button" @click="run(p.metadata.name)">{{ s.backups.run }}</button></td>
          </tr></tbody></table></div>
      </div>
    </LoadState>

    <RestoreWizard :open="!!restoring" :backup="restoring" @close="restoring = null" @done="l.reload" />
    <ConfirmDialog :open="!!deleting" :title="s.backups.deleteTitle(deleting?.spec.snapshot ?? '')" :confirm-label="s.backups.requestDelete" danger :run="requestDelete" @close="deleting = null">
      <Callout tone="warn">{{ s.backups.deleteText }}</Callout>
    </ConfirmDialog>
  </div>
</template>

<style>
.row-actions { display: flex; flex-wrap: wrap; gap: 6px; align-items: center; }
.row-actions select.depth { width: auto; min-width: 0; }
</style>
