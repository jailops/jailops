import { hasExt, session } from '@jailops/shared/composables/session'
import { projectState } from '@/composables/project'

// Who is offered Export and Import (extension jail_export). Exporting reads a jail's data: an operator of the project;
// importing makes a jail: an admin of the project. The server decides in the end; an identity whose role is unknown (a
// token without a session) may try.
function inScope(project: string): boolean {
  return !session.role || session.global || !project || session.projects.includes(project)
}
export function canExport(project: string = projectState.current): boolean {
  return hasExt('jail_export') && (!session.role || session.role === 'admin' || session.role === 'operator') && inScope(project)
}
export function canImport(project: string = projectState.current): boolean {
  return hasExt('jail_export') && (!session.role || session.role === 'admin') && inScope(project)
}
