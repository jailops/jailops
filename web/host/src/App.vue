<script setup lang="ts">
import { computed } from 'vue'
import { s } from '@/i18n'
import { watch } from 'vue'
import { can, isHub, hasExt, session } from '@jailops/shared/composables/session'
import ProjectSwitcher from '@/components/ProjectSwitcher.vue'
import { initProjects, projectState } from '@/composables/project'
import { useRoute } from 'vue-router'
import NoProject from '@/components/NoProject.vue'
import AppShell, { type NavItem } from '@jailops/shared/components/AppShell.vue'

const items = computed<NavItem[]>(() => ([
  { to: '/', label: s.nav.overview, icon: 'layoutDashboard', show: true },
  { to: '/jails', label: s.nav.jails, icon: 'box', show: can('jails') },
  { to: '/projects', label: s.nav.projects, icon: 'users', show: can('projects') },
  { to: '/storage', label: s.nav.storage, icon: 'hardDrive', show: can('storage') },
  { to: '/images', label: s.nav.images, icon: 'layers', show: can('images') },
  { to: '/apps', label: s.nav.apps, icon: 'store', show: can('apps') },
  { to: '/networks', label: s.nav.networks, icon: 'network', show: can('networks') },
  { to: '/firewall', label: s.nav.firewall, icon: 'shield', show: can('firewall') },
  { to: '/profiles', label: s.nav.profiles, icon: 'wrench', show: can('jails') },
  { to: '/updates', label: s.nav.updates, icon: 'arrowUp', show: hasExt('updates') },
  { to: '/backups', label: s.nav.backups, icon: 'archive', show: can('backups') },
  { to: '/host', label: s.nav.host, icon: 'monitor', show: hasExt('host_identity') },
  { to: '/hosts', label: s.nav.hosts, icon: 'server', show: isHub.value },
  { to: '/operations', label: s.nav.operations, icon: 'listChecks', show: can('operations') },
  { to: '/events', label: s.nav.events, icon: 'radio', show: can('events') },
  { to: '/doctor', label: s.nav.doctor, icon: 'activity', show: can('doctor') },
  { to: '/audit', label: s.nav.audit, icon: 'scrollText', show: can('audit') },
  { to: '/access', label: s.nav.access, icon: 'keyRound', show: true },
] as Array<NavItem & { show: boolean }>).filter((i) => i.show))
const route = useRoute()
/** A page that works inside a project needs one: there is no default project to fall back to. */
const needsProject = computed(() => !!route.meta.project && can('projects') && !projectState.current)
watch(() => session.authed, (a) => { if (a && can('projects')) void initProjects() }, { immediate: true })
</script>

<template>
  <AppShell :brand="s.app.name" :subtitle="s.app.role" :items="items" :events="can('events')">
    <template v-if="can('projects')" #header><ProjectSwitcher /></template>
    <NoProject v-if="needsProject" />
    <router-view v-else />
  </AppShell>
</template>
