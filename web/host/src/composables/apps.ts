import { computed, ref, watch } from 'vue'
import { s } from '@/i18n'
import { api } from '@/api/endpoints'
import { can, hasExt, session } from '@jailops/shared/composables/session'
import { currentProject, projectState } from '@/composables/project'
import { grants, installBlock } from '@/lib/apps'

/**
 * Whether the caller may install an app into the current project, and why not. The role comes from the
 * session (operator or admin, scoped to the project); the grants from the project. The server decides in the
 * end: this only avoids offering what it would refuse, and says what to grant.
 */
export function useInstallGate() {
  const allStorage = ref<string[]>([])
  // A storage grant of "*" means every storage of the host: only then is the host's list needed.
  watch(() => currentProject.value?.spec.storage?.some((g) => g.storage === '*'), (wild) => {
    if (wild && can('storage')) api.storage.list().then((l) => (allStorage.value = l.map((x) => x.metadata.name))).catch(() => undefined)
  }, { immediate: true })
  const granted = computed(() => grants(currentProject.value, allStorage.value))
  const block = computed(() => installBlock({
    ext: hasExt('app_install'), needProject: can('projects'), project: projectState.current, role: session.role, global: session.global,
    scopes: session.projects, known: !!currentProject.value, networks: granted.value.networks, storage: granted.value.storage,
  }))
  const reason = computed(() => {
    const p = projectState.current
    switch (block.value) {
      case 'noExtension': return s.apps.block.noExtension
      case 'noProject': return s.apps.block.noProject
      case 'role': return s.apps.block.role(session.role)
      case 'scope': return s.apps.block.scope(p)
      case 'noNetwork': return s.apps.block.noNetwork(p)
      case 'noStorage': return s.apps.block.noStorage(p)
    }
    return ''
  })
  /** The grants are missing: the project page is where they are added. */
  const toProject = computed(() => (block.value === 'noNetwork' || block.value === 'noStorage') && can('projects') ? `/projects/${projectState.current}` : '')
  return { granted, block, reason, toProject }
}
