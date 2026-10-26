/** Deep copy of plain data. structuredClone refuses Vue's reactive proxies, JSON does not. */
export function clone<T>(v: T): T {
  return v === undefined ? v : (JSON.parse(JSON.stringify(v)) as T)
}
