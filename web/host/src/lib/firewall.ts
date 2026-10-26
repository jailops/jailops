import type { FirewallRule } from '@jailops/shared/api/types'

export type RuleErrors = Record<string, string> // key: "<index>.<field>"

const PORT = /^\d{1,5}(-\d{1,5})?$/

export function splitList(s: string): string[] {
  return s.split(/[\s,]+/).map((x) => x.trim()).filter(Boolean)
}

export function validPort(p: string): boolean {
  if (!PORT.test(p)) return false
  return p.split('-').every((n) => Number(n) >= 1 && Number(n) <= 65535) && (!p.includes('-') || Number(p.split('-')[0]) <= Number(p.split('-')[1]))
}

/** Client-side checks that mirror the obvious server rules; the server stays the authority. */
export function validateRules(rules: FirewallRule[], msg: { portRange: string; portNeedsProto: string; redirectInOnly: string; hostPortRange: string }): RuleErrors {
  const e: RuleErrors = {}
  rules.forEach((r, i) => {
    if (r.ports?.length) {
      if (r.proto !== 'tcp' && r.proto !== 'udp') e[`${i}.ports`] = msg.portNeedsProto
      else if (!r.ports.every(validPort)) e[`${i}.ports`] = msg.portRange
    }
    if (r.redirect) {
      if (r.direction !== 'in') e[`${i}.redirect`] = msg.redirectInOnly
      else if (!(r.redirect.host_port >= 1 && r.redirect.host_port <= 65535) || !(r.redirect.jail_port >= 1 && r.redirect.jail_port <= 65535)) e[`${i}.redirect`] = msg.hostPortRange
    }
  })
  return e
}

/**
 * Map the detail of an api.Error to rule fields. The server names the failing field as
 * "rules[2].ports" or "rules.2.ports"; anything else stays in the error panel only.
 */
export function detailToRuleErrors(detail: Record<string, string> | undefined): RuleErrors {
  const out: RuleErrors = {}
  for (const [k, v] of Object.entries(detail ?? {})) {
    const m = /^(?:spec\.)?rules(?:\[(\d+)\]|\.(\d+))\.(\w+)/.exec(k)
    if (m) out[`${m[1] ?? m[2]}.${m[3]}`] = v
  }
  return out
}

export function blankRule(): FirewallRule {
  return { name: '', action: 'allow', direction: 'in', proto: 'tcp', from: [], to: [], ports: [] }
}
