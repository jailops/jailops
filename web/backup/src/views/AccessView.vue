<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import { s } from '@/i18n'
import { api } from '@/api/endpoints'
import { useLoad } from '@jailops/shared/composables/load'
import { runOp } from '@jailops/shared/composables/ops'
import { session } from '@jailops/shared/composables/session'
import { ApiError } from '@jailops/shared/api/client'
import { stamp } from '@jailops/shared/lib/format'
import Icon from '@jailops/shared/components/Icon.vue'
import Modal from '@jailops/shared/components/Modal.vue'
import ErrorPanel from '@jailops/shared/components/ErrorPanel.vue'
import LoadState from '@jailops/shared/components/LoadState.vue'
import TokenManager, { type TokenForm } from '@jailops/shared/components/TokenManager.vue'
import RoleNote from '@/components/RoleNote.vue'
import { allows } from '@/lib/roles'
import type { NodeRole, NodeUser } from '@/types'

const admin = computed(() => allows(session.role, 'admin'))
const l = useLoad(async () => (admin.value ? { users: await api.users.list(), tokens: await api.tokens.list() } : { users: [], tokens: [] }))
const tab = ref<'users' | 'tokens'>('users')
const mgr = ref<InstanceType<typeof TokenManager> | null>(null)
const roles: NodeRole[] = ['readonly', 'backup-admin', 'admin']
const roleOptions = roles.map((r) => ({ value: r, text: s.roles.text[r] }))
const rows = computed(() => (l.data.value?.tokens ?? []).map((t) => ({ name: t.name, role: t.role, expires_at: t.expires_at, allow_from: t.allow_from })))
const createToken = (f: TokenForm) => api.tokens.create({ name: f.name, role: f.role as NodeRole, ...(f.allow_from ? { allow_from: f.allow_from } : {}), ...(f.expires_at ? { expires_at: f.expires_at } : {}) })
const removeToken = (n: string) => api.tokens.remove(n)

const editing = ref<'new' | NodeUser | null>(null)
const form = reactive({ name: '', password: '', role: 'readonly' as NodeRole, disabled: false })
const busy = ref(false)
const error = ref<ApiError | null>(null)
function openUser(u: 'new' | NodeUser): void {
  editing.value = u
  Object.assign(form, u === 'new' ? { name: '', password: '', role: 'readonly', disabled: false } : { name: u.name, password: '', role: u.role, disabled: u.disabled })
  error.value = null
}
async function saveUser(): Promise<void> {
  busy.value = true
  error.value = null
  try {
    if (editing.value === 'new') await runOp(s.access.userTitle(form.name), () => api.users.create({ name: form.name, password: form.password, role: form.role }))
    else await runOp(s.access.editUser(form.name), () => api.users.update(form.name, { role: form.role, disabled: form.disabled, ...(form.password ? { password: form.password } : {}) }))
    editing.value = null
    await l.reload()
  } catch (e) {
    error.value = e instanceof ApiError ? e : new ApiError({ message: String(e) })
  } finally {
    busy.value = false
  }
}
const deleting = ref<NodeUser | null>(null)
const typed = ref('')
async function delUser(): Promise<void> {
  busy.value = true
  error.value = null
  try {
    await runOp(s.access.deleteTitle(deleting.value!.name), () => api.users.remove(deleting.value!.name))
    deleting.value = null
    await l.reload()
  } catch (e) {
    error.value = e instanceof ApiError ? e : new ApiError({ message: String(e) })
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div>
    <div class="page-head">
      <h1>{{ s.access.title }}</h1>
      <button v-if="admin && tab === 'users'" class="btn btn-primary" type="button" data-test="new-user" @click="openUser('new')"><Icon name="plus" />{{ s.access.newUser }}</button>
      <button v-if="admin && tab === 'tokens'" class="btn btn-primary" type="button" @click="mgr?.openCreate()"><Icon name="plus" />{{ s.access.newToken }}</button>
    </div>
    <RoleNote need="admin" gate />
    <section class="panel" style="margin-bottom: 12px">
      <header><h2>{{ s.access.rolesTitle }}</h2></header>
      <dl class="body kv"><template v-for="r in roles" :key="r"><dt class="mono">{{ r }}</dt><dd>{{ s.roles.text[r] }}</dd></template></dl>
    </section>
    <template v-if="admin">
      <div class="tabs" role="tablist">
        <button type="button" role="tab" :aria-selected="tab === 'users'" @click="tab = 'users'">{{ s.access.users }}</button>
        <button type="button" role="tab" :aria-selected="tab === 'tokens'" @click="tab = 'tokens'">{{ s.access.tokens }}</button>
      </div>
      <LoadState :loading="l.loading.value" :error="l.error.value" :has-data="!!l.data.value" @retry="l.reload">
        <div v-if="tab === 'users'" class="panel">
          <p v-if="!l.data.value?.users.length" class="empty">{{ s.access.noUsers }}</p>
          <div v-else class="table-wrap"><table>
            <thead><tr><th>{{ s.access.userName }}</th><th>{{ s.access.role }}</th><th class="hide-sm">{{ s.access.created }}</th><th></th></tr></thead>
            <tbody><tr v-for="u in l.data.value?.users" :key="u.name"><td class="mono">{{ u.name }}<span v-if="u.disabled" class="pill warn" style="margin-left: 6px">{{ s.access.disabled }}</span></td><td class="mono">{{ u.role }}</td><td class="faint hide-sm nowrap">{{ stamp(u.created_at) }}</td>
              <td class="nowrap"><button class="btn btn-sm" type="button" @click="openUser(u)" :aria-label="`${s.retention.edit} ${u.name}`">{{ s.retention.edit }}</button><button class="btn btn-sm btn-danger" type="button" :disabled="u.name === session.user" @click="deleting = u; typed = ''" :aria-label="`${s.access.deleteUser} ${u.name}`">{{ s.access.deleteUser }}</button></td></tr></tbody></table></div>
        </div>
        <TokenManager v-show="tab === 'tokens'" ref="mgr" :tokens="rows" :roles="roleOptions" :create="createToken" :remove="removeToken" @changed="l.reload" />
      </LoadState>
    </template>

    <Modal :open="!!editing" :title="editing === 'new' ? s.access.newUser : s.access.editUser(form.name)" @close="editing = null">
      <form id="us" @submit.prevent="saveUser">
        <div v-if="editing === 'new'" class="field"><label for="us-name">{{ s.access.userName }}</label><input id="us-name" v-model="form.name" class="mono" type="text" required /></div>
        <div class="field"><label for="us-pw">{{ editing === 'new' ? s.access.password : s.access.newPassword }}</label><input id="us-pw" v-model="form.password" type="password" autocomplete="new-password" :required="editing === 'new'" /></div>
        <div class="field"><label for="us-role">{{ s.access.role }}</label><select id="us-role" v-model="form.role"><option v-for="r in roles" :key="r" :value="r">{{ r }}</option></select><span class="hint">{{ s.roles.text[form.role] }}</span></div>
        <label v-if="editing !== 'new'" class="check"><input v-model="form.disabled" type="checkbox" />{{ s.access.disabled }}</label>
        <ErrorPanel v-if="error" :error="error" />
      </form>
      <template #footer><button class="btn" type="button" @click="editing = null">{{ s.app.cancel }}</button><button class="btn btn-primary" type="submit" form="us" :disabled="busy">{{ editing === 'new' ? s.access.createUser : s.access.save }}</button></template>
    </Modal>

    <Modal :open="!!deleting" :title="s.access.deleteTitle(deleting?.name ?? '')" @close="deleting = null">
      <form id="du" @submit.prevent="delUser">
        <div class="field"><label for="du-typed">{{ s.access.typeName(deleting?.name ?? '') }}</label><input id="du-typed" v-model="typed" class="mono" type="text" autocomplete="off" /></div>
        <ErrorPanel v-if="error" :error="error" />
      </form>
      <template #footer><button class="btn" type="button" @click="deleting = null">{{ s.app.cancel }}</button><button class="btn btn-danger" type="submit" form="du" :disabled="busy || typed !== deleting?.name">{{ s.access.deleteUser }}</button></template>
    </Modal>
  </div>
</template>
