// All copy of this console. Shared strings come from web/shared.
import { common } from '@jailops/shared/i18n/common'

export const en = {
  ...common,
  app: { ...common.app, name: 'JailOps', role: '__LABEL__' },
  nav: { overview: 'Overview', audit: 'Audit log' },
  overview: { title: 'Overview', empty: 'Nothing to show yet.' },
}

export type Strings = typeof en
