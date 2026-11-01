import { describe, expect, it } from 'vitest'
import en from '@/i18n/en.json'
import fa from '@/i18n/fa.json'
import { mergeTrees, s6Resources, type Tree } from '@/i18n/s6-resources'

function flatten(t: Tree, prefix = ''): string[] {
  return Object.entries(t).flatMap(([k, v]) => (typeof v === 'string' ? [`${prefix}${k}`] : flatten(v, `${prefix}${k}.`)))
}

describe('Sprint 6 i18n', () => {
  const s6 = s6Resources()
  it('the Sprint 6 pairs have identical key sets', () => {
    expect(flatten(s6.fa).sort()).toEqual(flatten(s6.en).sort())
  })
  it('the merged trees have identical key sets and no empty strings', () => {
    const me = mergeTrees(en as Tree, s6.en)
    const mf = mergeTrees(fa as Tree, s6.fa)
    expect(flatten(mf).sort()).toEqual(flatten(me).sort())
    const values = (t: Tree): string[] => Object.values(t).flatMap((v) => (typeof v === 'string' ? [v] : values(v)))
    expect(values(s6.en).every((v) => v.trim() !== '')).toBe(true)
    expect(values(s6.fa).every((v) => v.trim() !== '')).toBe(true)
  })
  it('does not shadow a base key with a different shape', () => {
    const clash = (base: Tree, extra: Tree, p = ''): string[] =>
      Object.entries(extra).flatMap(([k, v]) => {
        const cur = base[k]
        if (cur === undefined) return []
        if (typeof cur === 'string' || typeof v === 'string') return [`${p}${k}`]
        return clash(cur, v, `${p}${k}.`)
      })
    expect(clash(en as Tree, s6.en)).toEqual([])
    expect(clash(fa as Tree, s6.fa)).toEqual([])
  })
})
