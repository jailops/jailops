// Every path this console calls, in one module. The first path segment of each must be listed
// under "prefixes" for this app in web/apps.json (a test enforces it).
import { core } from '@jailops/shared/api/core'

export const api = {
  ...core,
}
