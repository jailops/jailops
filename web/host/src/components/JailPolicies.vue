<script setup lang="ts">
import { computed, ref } from 'vue'
import { s } from '@/i18n'
import { api } from '@/api/endpoints'
import { ApiError } from '@jailops/shared/api/client'
import { runOp } from '@jailops/shared/composables/ops'
import { session } from '@jailops/shared/composables/session'
import { useLoad } from '@jailops/shared/composables/load'
import Callout from '@jailops/shared/components/Callout.vue'
import ErrorPanel from '@jailops/shared/components/ErrorPanel.vue'
import Icon from '@jailops/shared/components/Icon.vue'
import type { ConfigChange, FirewallPolicy, ReachPolicy } from '@jailops/shared/api/types'
import type { ConfigResult } from '@/types'

// The policies of a jail in the order they are rendered, with one to attach or detach at a time: the change names the
// one policy and the server adds it to, or takes it out of, the list it has stored.
const props = defineProps<{ jail: string; policies: ReachPolicy[] }>()
const emit = defineEmits<{ changed: [] }>()
const all = useLoad<FirewallPolicy[]>(() => api.firewall.policies())
const error = ref<ApiError | null>(null)
const busy = ref(false)
const pick = ref('')

const block = computed(() => {
  const role = session.role
  return role && role !== 'operator' && role !== 'admin' ? s.policies.readOnly(role) : ''
})
// only a jail's own policies are attached here: the project's and the managed ones are changed where they come from
const free = computed(() => (all.data.value ?? []).map((p) => p.metadata.name).filter((n) => !props.policies.some((x) => x.name === n)))
const label = (p: ReachPolicy): string => (p.source === 'project' ? s.policies.project : p.source === 'managed' ? s.policies.managed : '')
const states = (p: ReachPolicy): string => {
  const d = [p.default_in ? s.policies.defaultIn(p.default_in) : '', p.default_out ? s.policies.defaultOut(p.default_out) : ''].filter(Boolean)
  return d.length ? d.join(', ') : '-'
}

async function change(ch: ConfigChange, title: string): Promise<void> {
  busy.value = true
  error.value = null
  try {
    await runOp<ConfigResult>(title, () => api.jailConfig.post(props.jail, ch))
    pick.value = ''
    emit('changed')
  } catch (e) {
    error.value = e instanceof ApiError ? e : new ApiError({ message: String(e) })
  } finally {
    busy.value = false
  }
}
const attach = (): Promise<void> => change({ add: { 'firewall.policies': pick.value } }, s.policies.attaching(pick.value, props.jail))
const detach = (name: string): Promise<void> => change({ remove: { 'firewall.policies': name } }, s.policies.detaching(name, props.jail))
</script>

<template>
  <div class="stack" data-test="jail-policies">
    <h3>{{ s.policies.title }}</h3>
    <Callout v-if="block" tone="info">{{ block }}</Callout>
    <p v-if="!policies.length" class="empty" data-test="policies-none">{{ s.policies.none }}</p>
    <div v-else class="table-wrap">
      <table class="rtable" data-test="policies-list">
        <thead><tr><th>#</th><th>{{ s.policies.policy }}</th><th>{{ s.policies.rules }}</th><th>{{ s.policies.states }}</th><th><span class="sr-only">{{ s.app.actions }}</span></th></tr></thead>
        <tbody>
          <tr v-for="(p, i) in policies" :key="p.name" :data-policy="p.name" :data-source="p.source">
            <td data-label="#">{{ i + 1 }}</td>
            <td :data-label="s.policies.policy"><router-link class="mono" :to="`/firewall?policy=${p.name}`">{{ p.name }}</router-link> <span v-if="label(p)" class="badge info">{{ label(p) }}</span></td>
            <td :data-label="s.policies.rules" class="mono">{{ p.rules }}</td>
            <td :data-label="s.policies.states" class="muted">{{ states(p) }}</td>
            <td>
              <button v-if="p.source === 'jail'" class="btn btn-sm" type="button" :disabled="busy || !!block" :data-test="`detach-${p.name}`" @click="detach(p.name)"><Icon name="trash" />{{ s.policies.detach }}</button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    <form class="row" data-test="policy-attach" @submit.prevent="attach">
      <label class="sr-only" for="policy-pick">{{ s.policies.attachLabel }}</label>
      <select id="policy-pick" v-model="pick" :disabled="busy || !!block || !free.length" data-test="policy-pick">
        <option value="" disabled>{{ free.length ? s.policies.choose : s.policies.noneLeft }}</option>
        <option v-for="n in free" :key="n" :value="n">{{ n }}</option>
      </select>
      <button class="btn btn-sm btn-primary" type="submit" :disabled="busy || !!block || !pick" data-test="policy-attach-go"><Icon name="plus" />{{ s.policies.attach }}</button>
    </form>
    <ErrorPanel v-if="error" :error="error" />
  </div>
</template>
