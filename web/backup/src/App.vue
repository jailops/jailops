<script setup lang="ts">
import { computed } from 'vue'
import { s } from '@/i18n'
import AppShell, { type NavItem } from '@jailops/shared/components/AppShell.vue'

const items = computed<NavItem[]>(() => ([
  { to: '/', label: s.nav.overview, icon: 'layoutDashboard', show: true },
  { to: '/hosts', label: s.nav.hosts, icon: 'server', show: true },
  { to: '/snapshots', label: s.nav.snapshots, icon: 'archive', show: true },
  { to: '/deletions', label: s.nav.deletions, icon: 'trash', show: true },
  { to: '/retention', label: s.nav.retention, icon: 'clock', show: true },
  { to: '/maintenance', label: s.nav.maintenance, icon: 'wrench', show: true },
  { to: '/audit', label: s.nav.audit, icon: 'scrollText', show: true },
  { to: '/access', label: s.nav.access, icon: 'keyRound', show: true },
] as Array<NavItem & { show: boolean }>).filter((i) => i.show))
</script>

<template>
  <AppShell :brand="s.app.name" :subtitle="s.app.role" :items="items" :events="false">
    <router-view />
  </AppShell>
</template>
