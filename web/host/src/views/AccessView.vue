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
import Callout from '@jailops/shared/components/Callout.vue'
import Modal from '@jailops/shared/components/Modal.vue'
import CopyField from '@jailops/shared/components/CopyField.vue'
import ErrorPanel from '@jailops/shared/components/ErrorPanel.vue'
import LoadState from '@jailops/shared/components/LoadState.vue'
import TokenManager, { type TokenForm } from '@jailops/shared/components/TokenManager.vue'
import ScopePicker from '@/components/ScopePicker.vue'
import { isGlobalAdmin } from '@/composables/project'
import { remoteAddCommand } from '@/lib/trust'
import type { Certificate, Permission, TrustToken, User } from '@jailops/shared/api/types'

type Tab = 'tokens' | 'trust' | 'certs' | 'users' | 'sessions' | 'whoami'
const tab = ref<Tab>('trust')
const admin = computed(() => isGlobalAdmin())
const l = useLoad(async () => {
  const [tokens, users, certs, projects, sessions, me] = await Promise.all([
    api.tokens.list().catch(() => []), api.access.users().catch(() => []), api.access.certificates().catch(() => []),
    api.projects.list().catch(() => []), api.trust.sessions().catch(() => []), api.trust.whoami().catch(() => undefined),
  ])
  return { tokens, users, certs, projects: projects.map((p) => p.metadata.name), sessions, me }
})
const names = computed(() => l.data.value?.projects ?? [])
const mgr = ref<InstanceType<typeof TokenManager> | null>(null)
const roles = (['readonly', 'operator', 'admin', 'backup-push', 'backup-pull', 'restore', 'backup-admin'] as const).map((r) => ({ value: r, text: s.access.roles[r] }))
const rows = computed(() => (l.data.value?.tokens ?? []).map((t) => ({ name: t.metadata.name, role: t.spec.permission, expires_at: t.spec.expires_at, allow_from: t.spec.allow_from, projects: t.spec.projects, global: t.spec.global })))
const createToken = (f: TokenForm) => api.tokens.create({ metadata: { name: f.name }, spec: { permission: f.role as Permission, ...(f.expires_at ? { expires_at: f.expires_at } : {}), ...(f.allow_from ? { allow_from: f.allow_from } : {}), ...(f.projects ? { projects: f.projects } : {}), ...(f.global ? { global: true } : {}) } })
const removeToken = (n: string) => api.tokens.remove(n)

// ---- trust token ----
const tf = reactive({ name: '', role: 'operator' as Permission, projects: [] as string[], global: false, ttl: 3600, expires: '' })
const scopeOk = computed(() => tf.global || tf.projects.length > 0)
const busy = ref(false)
const error = ref<ApiError | null>(null)
const issued = ref<TrustToken | null>(null)
async function createTrust(): Promise<void> {
  if (!scopeOk.value) return
  busy.value = true
  error.value = null
  try {
    const r = await runOp(s.trust.createTitle(tf.name), () => api.trust.createToken({ name: tf.name, permission: tf.role, ...(tf.global ? { global: true } : { projects: tf.projects }), ttl_seconds: tf.ttl, ...(tf.expires ? { expires_at: new Date(tf.expires + 'T23:59:59Z').toISOString() } : {}) }))
    issued.value = r.data as TrustToken
    tf.name = ''
  } catch (e) {
    error.value = e instanceof ApiError ? e : new ApiError({ message: String(e) })
  } finally {
    busy.value = false
  }
}
const command = computed(() => (issued.value ? remoteAddCommand(location.host, issued.value.token) : ''))

// ---- users ----
const nu = reactive({ name: '', role: 'operator' as Permission, projects: [] as string[], global: false, password: '' })
const creatingUser = ref(false)
const generated = ref<{ user: string; password: string } | null>(null)
function openNewUser(): void {
  Object.assign(nu, { name: '', role: 'operator', projects: [], global: false, password: '' })
  error.value = null
  creatingUser.value = true
}
async function createUser(): Promise<void> {
  busy.value = true
  error.value = null
  try {
    const r = await runOp(s.trust.newUserTitle, () => api.access.createUser({ metadata: { name: nu.name }, spec: { permission: nu.role, ...(nu.global ? { global: true } : { projects: nu.projects }) }, ...(nu.password ? { password: nu.password } : {}) }))
    creatingUser.value = false
    const out = r.data as { password?: string } | undefined
    if (out?.password) generated.value = { user: nu.name, password: out.password }
    nu.password = ''
    await l.reload()
  } catch (e) {
    error.value = e instanceof ApiError ? e : new ApiError({ message: String(e) })
  } finally {
    busy.value = false
  }
}
const pwUser = ref<User | null>(null)
const pw = ref('')
async function setPassword(): Promise<void> {
  busy.value = true
  error.value = null
  const name = pwUser.value!.metadata.name
  try {
    const r = await runOp(s.trust.passwordSet(name), () => api.access.setPassword(name, pw.value))
    pwUser.value = null
    const out = r.data as { password?: string } | undefined
    if (out?.password) generated.value = { user: name, password: out.password }
    pw.value = ''
  } catch (e) {
    error.value = e instanceof ApiError ? e : new ApiError({ message: String(e) })
  } finally {
    busy.value = false
  }
}
const roleUser = ref<User | null>(null)
const rf = reactive({ permission: 'operator' as Permission, projects: [] as string[], global: false })
function openRole(u: User): void {
  roleUser.value = u
  Object.assign(rf, { permission: u.spec.permission, projects: [...(u.spec.projects ?? [])], global: !!u.spec.global })
  error.value = null
}
async function saveRole(): Promise<void> {
  busy.value = true
  error.value = null
  const name = roleUser.value!.metadata.name
  try {
    await runOp(s.trust.roleTitle(name), () => api.access.patchUser(name, { permission: rf.permission, global: rf.global, projects: rf.global ? (null as unknown as string[]) : rf.projects }))
    roleUser.value = null
    await l.reload()
  } catch (e) {
    error.value = e instanceof ApiError ? e : new ApiError({ message: String(e) })
  } finally {
    busy.value = false
  }
}
const delUser = ref<User | null>(null)
async function deleteUser(): Promise<void> {
  busy.value = true
  error.value = null
  try {
    await runOp(s.trust.deleteUserTitle(delUser.value!.metadata.name), () => api.access.deleteUser(delUser.value!.metadata.name))
    delUser.value = null
    await l.reload()
  } catch (e) {
    error.value = e instanceof ApiError ? e : new ApiError({ message: String(e) })
  } finally {
    busy.value = false
  }
}

// ---- certificates ----
const editing = ref<Certificate | null>(null)
const ef = reactive({ permission: 'operator' as Permission, projects: [] as string[], global: false })
function openEdit(c: Certificate): void {
  editing.value = c
  Object.assign(ef, { permission: c.spec.permission, projects: [...(c.spec.projects ?? [])], global: !!c.spec.global })
  error.value = null
}
async function saveCert(): Promise<void> {
  busy.value = true
  error.value = null
  const c = editing.value!
  const spec = { ...c.spec, permission: ef.permission, projects: ef.global ? undefined : ef.projects, global: ef.global || undefined }
  try {
    await runOp(s.trust.editTitle(c.metadata.name), () => api.trust.putCertificate({ ...c, spec }))
    editing.value = null
    await l.reload()
  } catch (e) {
    error.value = e instanceof ApiError ? e : new ApiError({ message: String(e) })
  } finally {
    busy.value = false
  }
}
const revoking = ref<Certificate | null>(null)
const typed = ref('')
async function revoke(): Promise<void> {
  busy.value = true
  error.value = null
  try {
    await runOp(s.trust.revokeTitle(revoking.value!.metadata.name), () => api.access.deleteCertificate(revoking.value!.metadata.name))
    revoking.value = null
    await l.reload()
  } catch (e) {
    error.value = e instanceof ApiError ? e : new ApiError({ message: String(e) })
  } finally {
    busy.value = false
  }
}
const revSession = ref<import('@jailops/shared/api/types').Session | null>(null)
async function revokeSession(): Promise<void> {
  busy.value = true
  try {
    await runOp(s.trust.revokeSessionTitle(revSession.value!.user), () => api.trust.revokeSession(revSession.value!.id))
    revSession.value = null
    await l.reload()
  } catch (e) {
    error.value = e instanceof ApiError ? e : new ApiError({ message: String(e) })
  } finally {
    busy.value = false
  }
}
const scopeText = (x: { projects?: string[]; global?: boolean }) => (x.global ? '' : (x.projects ?? []).join(', '))
</script>

<template>
  <div>
    <div class="page-head"><h1>{{ s.access.title }}</h1><button v-if="tab === 'tokens'" class="btn btn-primary" type="button" @click="mgr?.openCreate()"><Icon name="plus" />{{ s.access.newToken }}</button><button v-if="tab === 'users'" class="btn btn-primary" type="button" :disabled="!admin" data-test="user-new" @click="openNewUser"><Icon name="plus" />{{ s.trust.newUser }}</button></div>
    <Callout v-if="!admin" style="margin-bottom: 12px">{{ s.trust.notAllowed }}</Callout>
    <div class="tabs" role="tablist">
      <button v-for="[k, label] in (Object.entries(s.trust.tabs) as Array<[Tab, string]>)" :key="k" type="button" role="tab" :aria-selected="tab === k" :data-tab="k" @click="tab = k">{{ label }}</button>
    </div>
    <LoadState :loading="l.loading.value" :error="l.error.value" :has-data="!!l.data.value" @retry="l.reload">
      <TokenManager v-show="tab === 'tokens'" ref="mgr" :tokens="rows" :roles="roles" :project-names="names" :create="createToken" :remove="removeToken" @changed="l.reload" />

      <section v-if="tab === 'trust'" class="panel" data-test="trust-panel">
        <header><h2>{{ s.trust.newTrust }}</h2></header>
        <form class="body" style="max-width: 520px" @submit.prevent="createTrust">
          <p class="muted">{{ s.trust.lead }}</p>
          <div class="field"><label for="tt-name">{{ s.trust.name }}</label><input id="tt-name" v-model="tf.name" class="mono" type="text" required pattern="[a-z0-9._\-]+" /></div>
          <div class="field"><label for="tt-role">{{ s.trust.role }}</label><select id="tt-role" v-model="tf.role"><option v-for="r in roles" :key="r.value" :value="r.value">{{ r.value }}</option></select><span class="hint">{{ s.access.roles[tf.role] }}</span></div>
          <ScopePicker v-model:projects="tf.projects" v-model:global="tf.global" :names="names" />
          <div class="field"><label for="tt-ttl">{{ s.trust.ttl }}</label><select id="tt-ttl" v-model.number="tf.ttl"><option v-for="(t, sec) in s.trust.ttls" :key="sec" :value="Number(sec)">{{ t }}</option></select></div>
          <div class="field"><label for="tt-exp">{{ s.trust.certExpiry }}</label><input id="tt-exp" v-model="tf.expires" type="date" /></div>
          <ErrorPanel v-if="error && !editing" :error="error" />
          <button class="btn btn-primary" type="submit" :disabled="busy || !tf.name || !scopeOk || !admin" data-test="trust-create">{{ s.trust.create }}</button>
        </form>
      </section>

      <div v-if="tab === 'certs'" class="panel">
        <p v-if="!l.data.value?.certs.length" class="empty">{{ s.trust.noCerts }}</p>
        <div v-else class="table-wrap"><table data-test="certs">
          <thead><tr><th>{{ s.jails.name }}</th><th>{{ s.trust.kind }}</th><th>{{ s.access.permission }}</th><th>{{ s.trust.scope }}</th><th class="hide-sm">{{ s.trust.expires }}</th><th></th></tr></thead>
          <tbody><tr v-for="c in l.data.value?.certs" :key="c.metadata.name" :data-cert="c.metadata.name">
            <td class="mono">{{ c.metadata.name }}<div class="faint mono" :title="c.spec.fingerprint">{{ c.spec.fingerprint.slice(0, 23) }}</div></td>
            <td>{{ c.spec.kind }}</td><td class="mono">{{ c.spec.permission }}</td>
            <td><span v-if="c.spec.global" class="badge warn" data-test="global-badge">{{ s.trust.global2 }}</span><template v-else><span v-for="p in c.spec.projects ?? []" :key="p" class="chip mono">{{ p }}</span><span v-if="!c.spec.projects?.length" class="pill danger">{{ s.trust.noScope }}</span></template></td>
            <td class="faint hide-sm nowrap">{{ c.spec.expires_at ? stamp(c.spec.expires_at) : '-' }}</td>
            <td class="nowrap"><button class="btn btn-sm" type="button" :disabled="!admin" @click="openEdit(c)">{{ s.trust.edit }}</button><button class="btn btn-sm btn-danger" type="button" :disabled="!admin" data-test="cert-revoke" @click="revoking = c; typed = ''; error = null">{{ s.trust.revoke }}</button></td>
          </tr></tbody></table></div>
      </div>

      <div v-if="tab === 'users'" class="panel">
        <p v-if="!l.data.value?.users.length" class="empty">{{ s.trust.noUsers }}</p>
        <div v-else class="table-wrap"><table>
          <thead><tr><th>{{ s.login.username }}</th><th>{{ s.access.permission }}</th><th>{{ s.trust.scope }}</th><th>{{ s.access.totp }}</th><th></th></tr></thead>
          <tbody><tr v-for="u in l.data.value?.users" :key="u.metadata.name"><td class="mono">{{ u.metadata.name }}</td><td class="mono">{{ u.spec.permission }}</td>
            <td><span v-if="u.spec.global" class="badge warn">{{ s.trust.global2 }}</span><template v-else>{{ scopeText(u.spec) || '-' }}</template></td>
            <td>{{ u.spec.totp ? 'on' : 'off' }}</td><td><span v-if="u.spec.disabled" class="pill warn">{{ s.access.disabled }}</span></td>
            <td class="nowrap"><button class="btn btn-sm" type="button" :disabled="!admin" data-test="user-role" @click="openRole(u)">{{ s.trust.setRole }}</button><button class="btn btn-sm" type="button" :disabled="!admin" data-test="user-password" @click="pwUser = u; pw = ''; error = null">{{ s.trust.setPassword }}</button><button class="btn btn-sm btn-danger" type="button" :disabled="!admin" data-test="user-delete" @click="delUser = u; typed = ''; error = null">{{ s.trust.deleteUser }}</button></td></tr></tbody></table></div>
      </div>

      <div v-if="tab === 'sessions'" class="panel">
        <p v-if="!l.data.value?.sessions.length" class="empty">{{ s.trust.noSessions }}</p>
        <div v-else class="table-wrap"><table data-test="sessions">
          <thead><tr><th>{{ s.trust.user }}</th><th class="hide-sm">{{ s.trust.remote }}</th><th class="hide-sm">{{ s.trust.created }}</th><th>{{ s.trust.lastSeen }}</th><th class="hide-sm">{{ s.trust.sessionExpires }}</th><th></th></tr></thead>
          <tbody><tr v-for="x in l.data.value?.sessions" :key="x.id" :data-session="x.id">
            <td class="mono">{{ x.user }}<span v-if="x.current" class="pill info" style="margin-left: 6px">{{ s.trust.current }}</span></td><td class="mono hide-sm">{{ x.remote || '-' }}</td>
            <td class="faint nowrap hide-sm">{{ stamp(x.created_at) }}</td><td class="faint nowrap">{{ stamp(x.last_seen) }}</td><td class="faint nowrap hide-sm">{{ stamp(x.expires_at) }}</td>
            <td><button class="btn btn-sm btn-danger" type="button" @click="revSession = x">{{ s.trust.revokeSession }}</button></td></tr></tbody></table></div>
      </div>

      <section v-if="tab === 'whoami'" class="panel" data-test="whoami-panel"><header><h2>{{ s.trust.whoami }}</h2></header>
        <dl v-if="l.data.value?.me" class="body kv">
          <dt>{{ s.trust.user }}</dt><dd class="mono">{{ l.data.value.me.user }}</dd>
          <dt>{{ s.trust.kindLabel }}</dt><dd class="mono">{{ l.data.value.me.kind }}</dd>
          <dt>{{ s.trust.permission }}</dt><dd class="mono">{{ l.data.value.me.permission }}</dd>
          <dt>{{ s.trust.scopeLabel }}</dt><dd><span v-if="l.data.value.me.global" class="badge warn">{{ s.trust.global2 }}</span><template v-else><span v-for="p in l.data.value.me.projects ?? []" :key="p" class="chip mono">{{ p }}</span><span v-if="!l.data.value.me.projects?.length" class="pill danger">{{ s.trust.noScope }}</span></template></dd>
        </dl><p v-else class="empty">-</p></section>
    </LoadState>

    <Modal :open="!!issued" :title="s.trust.createTitle(issued?.name ?? '')" wide @close="() => {}">
      <Callout tone="warn">{{ s.trust.shownOnce }}</Callout>
      <div class="field" style="margin-top: 12px"><span class="label">{{ s.trust.token }}</span><CopyField :value="issued?.token ?? ''" :label="s.trust.token" /></div>
      <div class="field"><span class="label">{{ s.trust.command }}</span><CopyField :value="command" label="command" /></div>
      <p v-if="issued" class="faint">{{ s.trust.expiresAt(stamp(issued.expires_at)) }}</p>
      <template #footer><button class="btn btn-primary" type="button" data-test="secret-done" @click="issued = null; l.reload()">{{ s.access.done }}</button></template>
    </Modal>

    <Modal :open="creatingUser" :title="s.trust.newUserTitle" @close="creatingUser = false">
      <form id="nu" @submit.prevent="createUser">
        <div class="field"><label for="nu-name">{{ s.trust.userName }}</label><input id="nu-name" v-model="nu.name" class="mono" type="text" required pattern="[a-z0-9._\-]+" autocomplete="off" /></div>
        <div class="field"><label for="nu-role">{{ s.trust.role }}</label><select id="nu-role" v-model="nu.role"><option v-for="r in roles" :key="r.value" :value="r.value">{{ r.value }}</option></select></div>
        <ScopePicker v-model:projects="nu.projects" v-model:global="nu.global" :names="names" />
        <div class="field"><label for="nu-pw">{{ s.trust.userPassword }}</label><input id="nu-pw" v-model="nu.password" type="password" autocomplete="new-password" /><span class="hint">{{ s.trust.passwordHint }}</span></div>
        <ErrorPanel v-if="error" :error="error" />
      </form>
      <template #footer><button class="btn" type="button" @click="creatingUser = false">{{ s.app.cancel }}</button><button class="btn btn-primary" type="submit" form="nu" :disabled="busy || !nu.name || !(nu.global || nu.projects.length)" data-test="user-create">{{ s.trust.createUser }}</button></template>
    </Modal>

    <Modal :open="!!pwUser" :title="s.trust.passwordSet(pwUser?.metadata.name ?? '')" @close="pwUser = null">
      <form id="up" @submit.prevent="setPassword">
        <p class="muted">{{ s.trust.passwordText }}</p>
        <div class="field"><label for="up-pw">{{ s.trust.userPassword }}</label><input id="up-pw" v-model="pw" type="password" autocomplete="new-password" /><span class="hint">{{ s.trust.passwordGenerate }}</span></div>
        <ErrorPanel v-if="error" :error="error" />
      </form>
      <template #footer><button class="btn" type="button" @click="pwUser = null">{{ s.app.cancel }}</button><button class="btn btn-primary" type="submit" form="up" :disabled="busy" data-test="user-password-go">{{ s.trust.setPassword }}</button></template>
    </Modal>

    <Modal :open="!!roleUser" :title="s.trust.roleTitle(roleUser?.metadata.name ?? '')" @close="roleUser = null">
      <form id="ur" @submit.prevent="saveRole">
        <div class="field"><label for="ur-role">{{ s.trust.role }}</label><select id="ur-role" v-model="rf.permission"><option v-for="r in roles" :key="r.value" :value="r.value">{{ r.value }}</option></select></div>
        <ScopePicker v-model:projects="rf.projects" v-model:global="rf.global" :names="names" />
        <ErrorPanel v-if="error" :error="error" />
      </form>
      <template #footer><button class="btn" type="button" @click="roleUser = null">{{ s.app.cancel }}</button><button class="btn btn-primary" type="submit" form="ur" :disabled="busy || !(rf.global || rf.projects.length)" data-test="user-role-go">{{ s.trust.saveRole }}</button></template>
    </Modal>

    <Modal :open="!!delUser" :title="s.trust.deleteUserTitle(delUser?.metadata.name ?? '')" @close="delUser = null">
      <form id="ud" @submit.prevent="deleteUser">
        <Callout tone="danger">{{ s.trust.deleteUserText }}</Callout>
        <div class="field" style="margin-top: 12px"><label for="ud-typed">{{ s.trust.typeName(delUser?.metadata.name ?? '') }}</label><input id="ud-typed" v-model="typed" class="mono" type="text" autocomplete="off" spellcheck="false" /></div>
        <ErrorPanel v-if="error" :error="error" />
      </form>
      <template #footer><button class="btn" type="button" @click="delUser = null">{{ s.app.cancel }}</button><button class="btn btn-danger" type="submit" form="ud" :disabled="busy || typed !== delUser?.metadata.name" data-test="user-delete-go">{{ s.trust.deleteUser }}</button></template>
    </Modal>

    <Modal :open="!!generated" :title="s.trust.generated" @close="() => {}">
      <Callout tone="warn">{{ s.trust.userShownOnce }}</Callout>
      <div class="field" style="margin-top: 12px"><span class="label">{{ generated?.user }}</span><CopyField :value="generated?.password ?? ''" :label="s.trust.userPassword" /></div>
      <template #footer><button class="btn btn-primary" type="button" data-test="user-secret-done" @click="generated = null">{{ s.trust.userDone }}</button></template>
    </Modal>

    <Modal :open="!!editing" :title="s.trust.editTitle(editing?.metadata.name ?? '')" @close="editing = null">
      <form id="ce" @submit.prevent="saveCert">
        <div class="field"><label for="ce-role">{{ s.trust.role }}</label><select id="ce-role" v-model="ef.permission"><option v-for="r in roles" :key="r.value" :value="r.value">{{ r.value }}</option></select></div>
        <ScopePicker v-model:projects="ef.projects" v-model:global="ef.global" :names="names" />
        <ErrorPanel v-if="error" :error="error" />
      </form>
      <template #footer><button class="btn" type="button" @click="editing = null">{{ s.app.cancel }}</button><button class="btn btn-primary" type="submit" form="ce" :disabled="busy || !(ef.global || ef.projects.length)" data-test="cert-save">{{ s.trust.save }}</button></template>
    </Modal>

    <Modal :open="!!revoking" :title="s.trust.revokeTitle(revoking?.metadata.name ?? '')" @close="revoking = null">
      <form id="cr" @submit.prevent="revoke">
        <Callout tone="danger">{{ s.trust.revokeText }}</Callout>
        <div class="field" style="margin-top: 12px"><label for="cr-typed">{{ s.trust.typeName(revoking?.metadata.name ?? '') }}</label><input id="cr-typed" v-model="typed" class="mono" type="text" autocomplete="off" spellcheck="false" /></div>
        <ErrorPanel v-if="error" :error="error" />
      </form>
      <template #footer><button class="btn" type="button" @click="revoking = null">{{ s.app.cancel }}</button><button class="btn btn-danger" type="submit" form="cr" :disabled="busy || typed !== revoking?.metadata.name" data-test="cert-revoke-go">{{ s.trust.revoke }}</button></template>
    </Modal>

    <Modal :open="!!revSession" :title="s.trust.revokeSessionTitle(revSession?.user ?? '')" @close="revSession = null">
      <Callout v-if="revSession?.current" tone="warn">{{ s.trust.revokeCurrent }}</Callout>
      <template #footer><button class="btn" type="button" @click="revSession = null">{{ s.app.cancel }}</button><button class="btn btn-danger" type="button" data-test="session-revoke-go" @click="revokeSession">{{ s.trust.revokeSession }}</button></template>
    </Modal>
  </div>
</template>
