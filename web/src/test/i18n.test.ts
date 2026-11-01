import { describe, expect, it } from 'vitest'
import en from '@/i18n/en.json'
import fa from '@/i18n/fa.json'

type Tree = { [k: string]: string | Tree }
function flatten(t: Tree, prefix = ''): string[] {
  return Object.entries(t).flatMap(([k, v]) =>
    typeof v === 'string' ? [`${prefix}${k}`] : flatten(v, `${prefix}${k}.`),
  )
}

describe('i18n', () => {
  it('en and fa have identical key sets', () => {
    expect(flatten(fa as Tree).sort()).toEqual(flatten(en as Tree).sort())
  })
  it('has no empty strings', () => {
    const empty = (t: Tree): boolean =>
      Object.values(t).some((v) => (typeof v === 'string' ? v.trim() === '' : empty(v)))
    expect(empty(en as Tree)).toBe(false)
    expect(empty(fa as Tree)).toBe(false)
  })
})
