import type { CatherdProfileDoc, CatherdRoleDoc } from '@/api/generated/model'

/** Pure edits of a catherd profile document. Every function returns a new document. */
type Doc = CatherdProfileDoc
type Obj = Record<string, unknown>

const clone = <T,>(v: T): T => structuredClone(v)

export function roleOf(doc: Doc, role: string): CatherdRoleDoc {
  return (doc.roles?.[role] as CatherdRoleDoc | undefined) ?? {}
}

export function setRole(doc: Doc, role: string, patch: Partial<CatherdRoleDoc>): Doc {
  const next = clone(doc)
  const roles = (next.roles ?? {}) as Record<string, CatherdRoleDoc>
  roles[role] = { ...roles[role], ...patch }
  next.roles = roles
  return next
}

export function addRung(doc: Doc, role: string, rung: string): Doc {
  const cur = roleOf(doc, role).rungs ?? []
  if (cur.includes(rung)) return doc
  return setRole(doc, role, { rungs: [...cur, rung] })
}

export function moveRung(doc: Doc, role: string, index: number, delta: -1 | 1): Doc {
  const cur = [...(roleOf(doc, role).rungs ?? [])]
  const to = index + delta
  if (to < 0 || to >= cur.length) return doc
  ;[cur[index], cur[to]] = [cur[to], cur[index]]
  return setRole(doc, role, { rungs: cur })
}

export function removeRung(doc: Doc, role: string, index: number): Doc {
  const cur = [...(roleOf(doc, role).rungs ?? [])]
  const [gone] = cur.splice(index, 1)
  const patch: Partial<CatherdRoleDoc> = { rungs: cur }
  if (roleOf(doc, role).defaultRung === gone) patch.defaultRung = null
  return setRole(doc, role, patch)
}

/** Sets or clears (value `undefined`/`''`) one key of a top-level object section such as `budget` or `timeouts`. */
export function setSection(doc: Doc, section: string, key: string, value: unknown): Doc {
  const next = clone(doc) as Obj
  const cur = { ...((next[section] as Obj | undefined) ?? {}) }
  if (value === undefined || value === '' || value === null) delete cur[key]
  else cur[key] = value
  next[section] = cur
  return next as Doc
}

export function setTop(doc: Doc, key: string, value: unknown): Doc {
  const next = clone(doc) as Obj
  if (value === undefined) delete next[key]
  else next[key] = value
  return next as Doc
}

/** Failover map row edit: `to` empty removes the stand-in. */
export function setFailover(doc: Doc, from: string, to: string): Doc {
  return setSection(doc, 'failover', from, to)
}

export function toggleNotify(doc: Doc, item: string, on: boolean): Doc {
  const cur = new Set(doc.notify ?? [])
  if (on) cur.add(item)
  else cur.delete(item)
  return setTop(doc, 'notify', [...cur])
}

/** All rungs mentioned by enabled roles, in role order, without repeats. */
export function allRungs(doc: Doc, roles: string[]): string[] {
  const out: string[] = []
  for (const r of roles) {
    const role = roleOf(doc, r)
    if (role.enabled === false) continue
    for (const x of role.rungs ?? []) if (!out.includes(x)) out.push(x)
  }
  return out
}

/** Parses the Advanced tab's JSON text. Returns the document or a parse message. */
export function parseDocText(text: string): { doc: Doc } | { error: string } {
  try {
    const v: unknown = JSON.parse(text)
    if (v === null || typeof v !== 'object' || Array.isArray(v)) return { error: 'object' }
    return { doc: v as Doc }
  } catch (e) {
    return { error: e instanceof Error ? e.message : 'invalid' }
  }
}
