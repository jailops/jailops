import { reactive, computed } from 'vue'
import { session } from '@jailops/shared/composables/session'
import { dataVersion } from '@jailops/shared/composables/events'
import { api } from '@/api/endpoints'
import type { Project } from '@jailops/shared/api/types'
import { withProject, loadChoice, saveChoice, orderProjects } from '@/lib/projectScope'

export const projectState = reactive({ current: '', projects: [] as Project[], loaded: false })

export const currentProject = computed(() => projectState.projects.find((p) => p.metadata.name === projectState.current))

/** Install the request filter. The project is the stored choice if it still exists; there is no default project, so
 *  without a choice nothing project-level is requested and the pages ask the user to choose or create one. */
export async function initProjects(): Promise<void> {
  projectState.current = loadChoice()
  api.setProjectFilter((path) => withProject(path, projectState.current))
  await refreshProjects()
}

export async function refreshProjects(): Promise<void> {
  if (!session.authed) return
  try {
    const list = orderProjects(await api.projects.list())
    projectState.projects = list
    projectState.loaded = true
    // A project that is gone (deleted, or no longer allowed to this identity) is forgotten, not replaced.
    if (projectState.current && !list.some((p) => p.metadata.name === projectState.current)) projectState.current = ''
    if (!projectState.current) saveChoice('')
  } catch {
    /* the switcher keeps what it has; the views show their own errors */
  }
}

export function switchProject(name: string): void {
  if (!name || name === projectState.current) return
  projectState.current = name
  saveChoice(name)
  dataVersion.value++ // every list reloads in the new project
}

/** Host-level writes (projects, networks, storage, trust) need a global admin. The server decides;
 *  the console only avoids offering what would be refused. An unknown identity may try. */
export function isGlobalAdmin(): boolean {
  if (!session.role) return true
  return session.global && session.role === 'admin'
}
