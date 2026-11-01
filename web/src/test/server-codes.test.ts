import { describe, expect, it } from 'vitest'
import en from '@/i18n/en.json'
import fa from '@/i18n/fa.json'
import { mergeTrees, s6Resources, type Tree } from '@/i18n/s6-resources'
import codes from './fixtures/server-codes.json'

// The Go census (internal/server/i18n_codes_test.go) lists every code the server can send; each needs a text in both
// languages so no raw code reaches the screen (Y19).
function has(tree: Tree, key: string): boolean {
  let cur: string | Tree | undefined = tree
  for (const part of key.split('.')) {
    if (cur === undefined || typeof cur === 'string') return false
    cur = cur[part]
  }
  return typeof cur === 'string' && cur.trim() !== ''
}

const s6 = s6Resources()
const merged = { en: mergeTrees(en as Tree, s6.en), fa: mergeTrees(fa as Tree, s6.fa) }

describe('server codes', () => {
  for (const lang of ['en', 'fa'] as const) {
    it(`every server code has a ${lang} text`, () => {
      const missing = (codes.keys as string[]).filter((k) => !has(merged[lang], k))
      expect(missing).toEqual([])
    })
  }
})

describe('policy help texts', () => {
  const labelKeys = (codes.keys as string[]).filter((k) => /^policy\.keys\.[a-z0-9_]+\.label$/.test(k))
  it('the census lists a label for every policy key', () => {
    expect(labelKeys.length).toBeGreaterThanOrEqual(77)
  })
  for (const lang of ['en', 'fa'] as const) {
    it(`every policy key has a ${lang} help text`, () => {
      const missing = labelKeys.map((k) => k.replace(/\.label$/, '.help')).filter((k) => !has(merged[lang], k))
      expect(missing).toEqual([])
    })
  }
})
