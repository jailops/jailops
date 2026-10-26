<script setup lang="ts">
import { s } from '@/i18n'
import { can } from '@jailops/shared/composables/session'
import AppShell, { type NavItem } from '@jailops/shared/components/AppShell.vue'

const items: NavItem[] = [{ to: '/', label: s.nav.overview, icon: 'layoutDashboard' }]
</script>

<template>
  <AppShell :brand="s.app.name" :subtitle="s.app.role" :items="items" :events="can('events')">
    <router-view />
  </AppShell>
</template>
