import type { AddressSet, AddressSetSpec, FirewallPolicy, FirewallRule } from '@jailops/shared/api/types'
import { ipv4, ipv6Groups } from './host'

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

// Address sets. The checks mirror internal/validate (SetName, AddressSetSpec, FlattenSet) as the server runs them on a
// create or a replace; the server stays the authority.

/** The rule of an address set name: it becomes part of a PF table name. */
export const SET_NAME = /^[a-z][a-z0-9_]{0,23}$/
export const SET_NAME_MAX = 24

/** The messages, named as in the firewall strings of the i18n file (pass s.firewall). */
export interface SetMsgs {
  setRequired: string
  setTooLong: string
  setBadName: string
  setExists: string
  setMember: (a: string) => string
  setIncludeName: (n: string) => string
  setMissing: (n: string) => string
  setCycle: (path: string) => string
}

/** The fields of the set form; a key is set only when the field has a problem. */
export type SetErrors = Partial<Record<'name' | 'addresses' | 'includes', string>>

function v4Bytes(s: string): number[] | null {
  return ipv4(s) ? s.split('.').map(Number) : null
}

/**
 * An address or a CIDR as the server parses a member: IPv4 without leading zeros, IPv6 with no zone and not in
 * the IPv4-mapped form, a prefix length without a sign or leading zeros of at most 32 or 128. Host bits may be
 * set (the server masks them).
 */
export function setMember(s: string): boolean {
  const slash = s.lastIndexOf('/')
  const addr = slash < 0 ? s : s.slice(0, slash)
  let bits = 32
  if (!v4Bytes(addr)) {
    const g = ipv6Groups(addr)
    if (!g || (g.slice(0, 5).every((x) => x === 0) && g[5] === 0xffff)) return false
    bits = 128
  }
  if (slash < 0) return true
  const len = s.slice(slash + 1)
  return /^(0|[1-9][0-9]{0,2})$/.test(len) && Number(len) <= bits
}

/** Empty when the name may be created, else the message under the field. */
export function setNameError(name: string, taken: string[], msg: SetMsgs): string {
  if (!name) return msg.setRequired
  if (name.length > SET_NAME_MAX) return msg.setTooLong
  if (!SET_NAME.test(name)) return msg.setBadName
  if (taken.includes(name)) return msg.setExists
  return ''
}

/** The spec the form sends: members one per line (commas and spaces also separate), includes comma separated. */
export function setSpec(members: string, includes: string): AddressSetSpec {
  const inc = splitList(includes)
  return inc.length ? { addresses: splitList(members), includes: inc } : { addresses: splitList(members) }
}

/**
 * The problems of a set the server would refuse: a member that is no address or CIDR, an include that is no set
 * name, does not exist or closes a cycle. `others` are the sets of the project; an empty set is allowed.
 */
export function validateSet(name: string, spec: AddressSetSpec, others: Record<string, AddressSetSpec>, msg: SetMsgs): SetErrors {
  const e: SetErrors = {}
  const bad = (spec.addresses ?? []).find((a) => !setMember(a))
  if (bad !== undefined) e.addresses = msg.setMember(bad)
  const badInc = (spec.includes ?? []).find((n) => !SET_NAME.test(n))
  if (badInc !== undefined) e.includes = msg.setIncludeName(badInc)
  else if (name) {
    const sets = { ...others, [name]: spec }
    const walk = (n: string, path: string[]): string => {
      if (path.includes(n)) return msg.setCycle([...path, n].join(' -> '))
      const s = sets[n]
      if (!s) return msg.setMissing(n)
      for (const i of s.includes ?? []) {
        const p = walk(i, [...path, n])
        if (p) return p
      }
      return ''
    }
    const p = walk(name, [])
    if (p) e.includes = p
  }
  return e
}

/** What refers to a set: the policies with @name in a rule and the sets that include it. The server refuses to
 *  delete a set while either list has entries. */
export function setUsers(name: string, policies: FirewallPolicy[], sets: AddressSet[]): { policies: string[]; sets: string[] } {
  const ref = '@' + name
  return {
    policies: policies.filter((p) => (p.spec.rules ?? []).some((r) => r.from?.includes(ref) || r.to?.includes(ref))).map((p) => p.metadata.name),
    sets: sets.filter((x) => x.spec.includes?.includes(name)).map((x) => x.metadata.name),
  }
}

/** The field a server error names (detail.field of a validation error), with the server's message. */
export function detailToSetErrors(e: { message: string; detail?: Record<string, string> } | null | undefined): SetErrors {
  const f = e?.detail?.field
  return f === 'name' || f === 'addresses' || f === 'includes' ? { [f]: e!.message } : {}
}
