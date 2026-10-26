import type { ServerInfo } from '@jailops/shared/api/types'

export function server(): ServerInfo {
  return {
    api_version: '1.0', api_extensions: ['operations', 'events'], version: '0.1.0-demo', host_uuid: '00000000-0000-4000-8000-000000000000',
    hostname: 'demo.example.net', role: '__ROLE__' as ServerInfo['role'], auth: 'trusted',
    environment: { os: 'FreeBSD', kernel_version: '15.1-RELEASE', arch: 'amd64' },
  }
}
