<script setup lang="ts">
import { computed, ref } from 'vue'
import Icon from '@jailops/shared/components/Icon.vue'
import NetTabs from '@/components/NetTabs.vue'
import WireGuardServerForm from '@/components/WireGuardServerForm.vue'
import WireGuardPeerDialog from '@/components/WireGuardPeerDialog.vue'
import WireGuardJoinDialog from '@/components/WireGuardJoinDialog.vue'
import { isGlobalAdmin } from '@/composables/project'
import { s } from '@/i18n'
import { api } from '@/api/endpoints'
import { useLoad } from '@jailops/shared/composables/load'
import { runOp } from '@jailops/shared/composables/ops'
import { ago, bytes } from '@jailops/shared/lib/format'
import AddrCopy from '@jailops/shared/components/AddrCopy.vue'
import Callout from '@jailops/shared/components/Callout.vue'
import ConfirmDialog from '@jailops/shared/components/ConfirmDialog.vue'
import LoadState from '@jailops/shared/components/LoadState.vue'
import Modal from '@jailops/shared/components/Modal.vue'
import { handshakeLabel, peerRows, reach } from '@/lib/wireguard'
import type { WireGuard, WireGuardKeys } from '@jailops/shared/api/types'

const l = useLoad(async () => {
  const tunnels = await api.wireguard.list()
  const nets = (await api.networks.list().catch(() => [])).map((n) => n.metadata.name)
  return { tunnels, nets }
})
const canWrite = computed(() => isGlobalAdmin())
const creating = ref(false)
const joining = ref(false)
const peerOf = ref('')
const deleting = ref<WireGuard | null>(null)
const removingPeer = ref<{ server: string; peer: string } | null>(null)
const rotating = ref<WireGuard | null>(null)
const rotated = ref<(WireGuardKeys & { name: string }) | null>(null)

const hs = (ts: string | undefined) => handshakeLabel(ts, (t) => ago(t), s.wireguard.never)

async function setUp(w: WireGuard, up: boolean): Promise<void> {
  const n = w.metadata.name
  await runOp(`${up ? s.wireguard.bringUp : s.wireguard.takeDown}: ${n}`, () => (up ? api.wireguard.up(n) : api.wireguard.down(n))).catch(() => undefined)
  await l.reload()
}
const removeTunnel = () => api.wireguard.remove(deleting.value?.metadata.name ?? '')
const removePeer = () => api.wireguard.removePeer(removingPeer.value?.server ?? '', removingPeer.value?.peer ?? '')
async function rotate(): Promise<void> {
  const w = rotating.value!
  const r = await api.wireguard.rotateKeys(w.metadata.name)
  if (r.kind === 'sync') rotated.value = { ...r.data, name: w.metadata.name }
  await l.reload()
}
</script>

<template>
  <div>
    <div class="page-head">
      <div><h1>{{ s.wireguard.title }}</h1><p class="muted">{{ s.wireguard.lead }}</p></div>
      <div class="row">
        <button class="btn" type="button" :disabled="!canWrite" data-test="wg-join" @click="joining = true"><Icon name="plus" />{{ s.wireguard.join }}</button>
        <button class="btn btn-primary" type="button" :disabled="!canWrite" data-test="wg-new" @click="creating = true"><Icon name="plus" />{{ s.wireguard.create }}</button>
      </div>
    </div>
    <NetTabs />
    <LoadState :loading="l.loading.value" :error="l.error.value" :has-data="!!l.data.value" @retry="l.reload">
      <p v-if="!l.data.value?.tunnels.length" class="empty">{{ s.wireguard.empty }}</p>
      <div class="stack">
        <section v-for="w in l.data.value?.tunnels" :key="w.metadata.name" class="panel" :data-wg="w.metadata.name">
          <header>
            <div class="row">
              <h2 class="mono">{{ w.metadata.name }}</h2>
              <span class="badge" :class="w.spec.mode === 'server' ? 'info' : 'warn'" :data-wg-mode="w.spec.mode">{{ w.spec.mode === 'server' ? s.wireguard.server : s.wireguard.client }}</span>
              <span class="pill" :class="w.status.up ? 'ok' : 'danger'" data-test="wg-state">{{ w.status.up ? s.wireguard.up : s.wireguard.down }}</span>
              <span v-if="w.spec.mode === 'server'" class="faint">{{ s.wireguard.peersCount(w.spec.peers?.length ?? 0) }}</span>
            </div>
          </header>
          <div class="body stack">
            <dl class="kv">
              <dt>{{ s.wireguard.iface }}</dt><dd class="mono">{{ w.spec.interface ?? w.status.interface }}</dd>
              <dt>{{ s.wireguard.address }}</dt><dd><AddrCopy v-for="a in w.spec.address" :key="a" :value="a" style="margin-right: 8px" /></dd>
              <dt>{{ s.wireguard.reach }}</dt><dd class="mono">{{ reach(w) }}</dd>
              <dt>{{ s.wireguard.publicKey }}</dt><dd><AddrCopy :value="w.status.public_key" /></dd>
              <template v-if="w.spec.networks?.length"><dt>{{ s.wireguard.networks }}</dt><dd><span v-for="n in w.spec.networks" :key="n" class="chip mono">{{ n }}</span></dd></template>
            </dl>
            <p v-if="w.spec.down" class="muted">{{ s.wireguard.downHint }}</p>
            <Callout v-if="w.status.firewall_rule" tone="info">
              <strong>{{ s.wireguard.firewallRule }}</strong>: <code class="mono">{{ w.status.firewall_rule }}</code><br /><span class="muted">{{ s.wireguard.firewallHint }}</span>
            </Callout>
            <Callout v-for="x in w.status.warnings ?? []" :key="x" tone="warn">{{ x }}</Callout>
            <div>
              <div class="row" style="justify-content: space-between"><h3>{{ s.wireguard.peers }}</h3>
                <button v-if="w.spec.mode === 'server'" class="btn btn-sm" type="button" :disabled="!canWrite" data-test="wg-add-peer" @click="peerOf = w.metadata.name"><Icon name="plus" />{{ s.wireguard.addPeer }}</button></div>
              <p v-if="!peerRows(w).length" class="muted">{{ s.wireguard.noPeers }}</p>
              <div v-else class="table-wrap">
                <table data-test="wg-peers">
                  <thead><tr><th>{{ s.wireguard.peerName }}</th><th>{{ s.wireguard.peerAddress }}</th><th>{{ s.wireguard.handshake }}</th><th class="hide-sm">{{ s.wireguard.transfer }}</th><th v-if="w.spec.mode === 'server'"><span class="sr-only">{{ s.wireguard.removePeer }}</span></th></tr></thead>
                  <tbody>
                    <tr v-for="p in peerRows(w)" :key="p.publicKey" :data-peer="p.name">
                      <td class="mono">{{ p.name }}</td>
                      <td class="mono">{{ p.address }}</td>
                      <td class="nowrap" data-test="wg-handshake">{{ hs(p.handshake) }}</td>
                      <td class="mono nowrap hide-sm">{{ bytes(p.rx ?? 0) }} / {{ bytes(p.tx ?? 0) }}</td>
                      <td v-if="w.spec.mode === 'server'"><button class="btn btn-sm btn-danger" type="button" :disabled="!canWrite" :aria-label="`${s.wireguard.removePeer} ${p.name}`" @click="removingPeer = { server: w.metadata.name, peer: p.name }">{{ s.wireguard.removePeer }}</button></td>
                    </tr>
                  </tbody>
                </table>
              </div>
            </div>
            <div class="row">
              <button v-if="w.spec.down" class="btn btn-sm" type="button" :disabled="!canWrite" data-test="wg-up" @click="setUp(w, true)">{{ s.wireguard.bringUp }}</button>
              <button v-else class="btn btn-sm" type="button" :disabled="!canWrite" data-test="wg-down" @click="setUp(w, false)">{{ s.wireguard.takeDown }}</button>
              <button class="btn btn-sm" type="button" :disabled="!canWrite" data-test="wg-rotate" @click="rotating = w">{{ s.wireguard.rotate }}</button>
              <button class="btn btn-sm btn-danger" type="button" :disabled="!canWrite" data-test="wg-delete" @click="deleting = w">{{ s.wireguard.delete }}</button>
            </div>
          </div>
        </section>
      </div>
    </LoadState>

    <WireGuardServerForm :open="creating" :networks="l.data.value?.nets ?? []" @close="creating = false" @done="l.reload" />
    <WireGuardJoinDialog :open="joining" @close="joining = false" @done="l.reload" />
    <WireGuardPeerDialog :open="!!peerOf" :server="peerOf" @close="peerOf = ''" @done="l.reload" />
    <ConfirmDialog :open="!!deleting" :title="s.wireguard.deleteTitle(deleting?.metadata.name ?? '')" :confirm-label="s.wireguard.delete" danger
      :run="removeTunnel" @close="deleting = null" @done="l.reload">
      <Callout tone="warn">{{ s.wireguard.deleteText(deleting?.metadata.name ?? '') }}</Callout>
    </ConfirmDialog>
    <ConfirmDialog :open="!!removingPeer" :title="s.wireguard.removePeerTitle(removingPeer?.peer ?? '')" :confirm-label="s.wireguard.removePeer" danger
      :run="removePeer" @close="removingPeer = null" @done="l.reload">
      <Callout tone="warn">{{ s.wireguard.removePeerText(removingPeer?.peer ?? '', removingPeer?.server ?? '') }}</Callout>
    </ConfirmDialog>
    <ConfirmDialog :open="!!rotating" :title="s.wireguard.rotateTitle(rotating?.metadata.name ?? '')" :confirm-label="s.wireguard.rotate" :run="rotate" @close="rotating = null">
      <Callout tone="warn">{{ s.wireguard.rotateText }}</Callout>
    </ConfirmDialog>
    <Modal :open="!!rotated" :title="s.wireguard.rotated" @close="rotated = null">
      <p class="mono" data-test="wg-rotated-key">{{ rotated?.public_key }}</p>
      <Callout v-if="rotated?.warning" tone="warn">{{ rotated.warning }}</Callout>
      <template #footer><button class="btn btn-primary" type="button" @click="rotated = null">{{ s.app.close }}</button></template>
    </Modal>
  </div>
</template>
