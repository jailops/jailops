<script setup lang="ts">
import { computed, ref } from 'vue'
import { s } from '@/i18n'
import { api } from '@/api/endpoints'
import { useLoad } from '@jailops/shared/composables/load'
import { session } from '@jailops/shared/composables/session'
import { projectState } from '@/composables/project'
import { stamp } from '@jailops/shared/lib/format'
import LoadState from '@jailops/shared/components/LoadState.vue'
import Callout from '@jailops/shared/components/Callout.vue'
import ConfirmDialog from '@jailops/shared/components/ConfirmDialog.vue'
import Icon from '@jailops/shared/components/Icon.vue'
import AppAllowDialog from '@/components/AppAllowDialog.vue'
import type { AppAccessGrant, Jail } from '@jailops/shared/api/types'

// Who may connect to an installed app: its grants, what each one generated, and Allow and Deny.
const props = defineProps<{ jail: Jail }>()
const l = useLoad(() => api.appAccess.get(props.jail.metadata.name))
const acc = computed(() => l.data.value)

// The server decides; this only avoids offering what it would refuse.
const block = computed(() => {
  const r = session.role
  if (r && r !== 'operator' && r !== 'admin') return s.appAccess.readOnly(r)
  const p = projectState.current
  if (r && !session.global && p && !session.projects.includes(p)) return s.appAccess.scope(p)
  return ''
})

const allowOpen = ref(false)
const denying = ref<AppAccessGrant | null>(null)
const dropRole = ref(false)
function askDeny(g: AppAccessGrant): void {
  dropRole.value = false
  denying.value = g
}
const runDeny = () => api.appAccess.deny(props.jail.metadata.name, { source: denying.value!.source, drop_role: dropRole.value || undefined })
</script>

<template>
  <section class="panel" aria-labelledby="acc-h" data-test="access-tab">
    <header>
      <h2 id="acc-h">{{ s.appAccess.title }}</h2>
      <span :title="block || undefined">
        <button class="btn btn-sm btn-primary" type="button" :disabled="!!block || !acc" data-test="allow-open" @click="allowOpen = true"><Icon name="plus" />{{ s.appAccess.allow }}</button>
      </span>
    </header>
    <div class="body stack">
      <p class="muted">{{ s.appAccess.lead }}</p>
      <Callout v-if="block" tone="info" data-test="access-blocked">{{ block }}</Callout>
      <LoadState :loading="l.loading.value" :error="l.error.value" :has-data="!!acc" @retry="l.reload">
        <div v-if="acc" class="stack">
          <dl class="kv">
            <dt>{{ s.firewall.policy }}</dt>
            <dd><span class="pill" :class="acc.closed ? 'ok' : 'warn'" data-test="access-state">{{ acc.closed ? s.appAccess.closed : s.appAccess.open }}</span> <span class="muted">{{ acc.closed ? s.appAccess.closedText(acc.policy) : s.appAccess.openText }}</span></dd>
            <dt>{{ s.appAccess.defaultPorts }}</dt><dd class="mono">{{ acc.ports.join(', ') || '-' }}</dd>
            <dt>{{ s.appAccess.type }}</dt><dd>{{ acc.type ? s.appAccess.typeText(acc.type) : s.appAccess.firewallOnly }}</dd>
          </dl>
          <Callout v-for="w in acc.warnings ?? []" :key="w" tone="warn">{{ w }}</Callout>
          <p v-if="!acc.grants.length" class="empty" data-test="access-empty">{{ s.appAccess.none }}</p>
          <ul v-else class="acc-grants" data-test="access-grants">
            <li v-for="g in acc.grants" :key="g.source" class="acc-grant" :data-grant="g.source" :data-drift="!!g.problems?.length">
              <div class="acc-head">
                <div class="acc-what">
                  <span class="mono acc-src">{{ g.source }}</span>
                  <span class="badge info">{{ s.appAccess.kinds[g.source_kind] ?? g.source_kind }}</span>
                  <span v-if="g.problems?.length" class="pill warn">{{ s.appAccess.problems }}</span>
                  <span v-else class="pill ok">{{ s.appAccess.ok }}</span>
                </div>
                <button class="btn btn-sm" type="button" :disabled="!!block" :data-test="`deny-${g.source}`" @click="askDeny(g)"><Icon name="trash" />{{ s.appAccess.deny }}</button>
              </div>
              <dl class="kv">
                <dt>{{ s.appAccess.ports }}</dt><dd class="mono">{{ g.ports.join(', ') }}</dd>
                <template v-if="g.database"><dt>{{ s.appAccess.database }}</dt><dd class="mono">{{ g.database }} <span class="faint">({{ s.appAccess.roleOf(g.role || g.database) }})</span></dd></template>
                <dt>{{ s.appAccess.created }}</dt><dd>{{ s.appAccess.createdBy(stamp(g.created_at), g.created_by ?? '') }}</dd>
              </dl>
              <Callout v-if="g.problems?.length" tone="warn" data-test="grant-problems"><ul class="plain"><li v-for="p in g.problems" :key="p">{{ p }}</li></ul></Callout>
              <details class="acc-rules">
                <summary>{{ s.appAccess.rules }}<span class="sr-only"> {{ s.appAccess.rulesFor(g.source) }}</span></summary>
                <dl class="kv">
                  <dt>{{ s.appAccess.typed }}</dt><dd><div v-for="r in g.rules" :key="r" class="mono">{{ r }}</div></dd>
                  <template v-if="g.pf?.length"><dt>{{ s.appAccess.pf }}</dt><dd><div v-for="r in g.pf" :key="r" class="mono">{{ r }}</div></dd></template>
                  <template v-if="g.hba?.length"><dt>{{ s.appAccess.hba }}</dt><dd><div v-for="r in g.hba" :key="r" class="mono">{{ r }}</div></dd></template>
                </dl>
              </details>
            </li>
          </ul>
        </div>
      </LoadState>
    </div>
    <AppAllowDialog v-if="acc" :open="allowOpen" :access="acc" @close="allowOpen = false" @done="l.reload" />
    <ConfirmDialog :open="!!denying" :title="denying ? s.appAccess.denyTitle(denying.source) : ''" :confirm-label="s.appAccess.deny" danger :run="runDeny" @close="denying = null" @done="l.reload">
      <div v-if="denying" class="stack" data-test="deny-dialog">
        <p>{{ s.appAccess.denyText(denying.source, jail.metadata.name) }}</p>
        <template v-if="denying.role">
          <p class="muted">{{ s.appAccess.denyRoleKeep(denying.role) }}</p>
          <label class="acc-kind"><input v-model="dropRole" type="checkbox" data-test="drop-role" /> {{ s.appAccess.dropRole }}</label>
        </template>
      </div>
    </ConfirmDialog>
  </section>
</template>

<style>
.acc-grants { list-style: none; margin: 0; padding: 0; display: grid; gap: 10px; }
.acc-grant { border: 1px solid var(--border); border-radius: var(--radius); padding: 8px 12px; display: grid; gap: 6px; min-width: 0; }
.acc-grant[data-drift='true'] { border-color: var(--warn); }
.acc-head { display: flex; justify-content: space-between; gap: 8px; align-items: center; flex-wrap: wrap; }
.acc-what { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; min-width: 0; }
.acc-src { font-weight: 500; overflow-wrap: anywhere; }
.acc-rules summary { cursor: pointer; color: var(--text-2); }
.acc-rules .mono { overflow-wrap: anywhere; font-size: 12px; }
</style>
