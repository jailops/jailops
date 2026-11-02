// The name rules of the server, in one place, with the hint every form shows. The server is the judge: these
// only say early what it would refuse. Keep them in step with internal/validate/names.go (Name),
// internal/validate/project.go (ProjectName), internal/service/resources.go (ValidObjectName),
// internal/auth/trust.go and internal/wireguard (the interface wg-<name> has at most 15 characters).

export type NameRule = 'object' | 'project' | 'tunnel' | 'identity' | 'client' | 'node'

interface Rule {
  re: RegExp
  max: number
  hint: string
  reserved?: string[]
}

const objectHint = 'Lowercase letters, digits and hyphens, starting with a letter, at most 63 characters.'

/** The rules by name. `object` covers jails, app instances, networks, volumes, storage, profiles and firewall policies. */
export const nameRules: Record<NameRule, Rule> = {
  object: { re: /^[a-z][a-z0-9-]{0,62}$/, max: 63, hint: objectHint },
  project: { re: /^[a-z][a-z0-9-]{0,62}$/, max: 63, hint: objectHint + ' Not "all" or "host".', reserved: ['all', 'host'] },
  tunnel: { re: /^[a-z][a-z0-9-]{0,11}$/, max: 12, hint: 'Lowercase letters, digits and hyphens, starting with a letter, at most 12 characters: the interface is wg-<name>.' },
  identity: { re: /^[a-z0-9_-][a-z0-9._-]{0,127}$/, max: 128, hint: "Lowercase letters, digits, '-', '_' and '.', not starting with '.', at most 128 characters." },
  // the backup node (internal/backupd): host, user and token names are free text of 1 to 128 characters
  node: { re: /^[^\s].{0,127}$/s, max: 128, hint: 'Up to 128 characters, not starting with a space.' },
  client: { re: /^[a-z0-9][a-z0-9._-]{0,127}$/, max: 128, hint: "Lowercase letters, digits, '-', '_' and '.', starting with a letter or digit, at most 128 characters." },
}

/** The hint shown under a name field. */
export function nameHint(rule: NameRule = 'object'): string {
  return nameRules[rule].hint
}

/** Empty when the name passes the rule, else the sentence to show under the field. An empty name is "required". */
export function nameProblem(v: string, rule: NameRule = 'object'): string {
  const r = nameRules[rule]
  if (!v) return 'Required.'
  if (v.length > r.max) return `At most ${r.max} characters.`
  if (r.reserved?.includes(v)) return `"${v}" is reserved.`
  if (!r.re.test(v)) return r.hint
  return ''
}

export function validName(v: string, rule: NameRule = 'object'): boolean {
  return nameProblem(v, rule) === ''
}
