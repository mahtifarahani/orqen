// Sprint 6 strings live in i18n/s6/*.{en,fa}.json (one pair per feature) and are merged into the base trees here, so
// the base en.json/fa.json stay untouched. Existing keys always win; a test asserts the merged trees have identical keys.
export type Tree = { [k: string]: string | Tree }

export function mergeTrees(base: Tree, extra: Tree): Tree {
  const out: Tree = { ...base }
  for (const [k, v] of Object.entries(extra)) {
    const cur = out[k]
    if (cur === undefined) out[k] = v
    else if (typeof cur !== 'string' && typeof v !== 'string') out[k] = mergeTrees(cur, v)
  }
  return out
}

function collect(mods: Record<string, unknown>): Tree {
  let acc: Tree = {}
  for (const path of Object.keys(mods).sort()) acc = mergeTrees(acc, (mods[path] as { default: Tree }).default)
  return acc
}

export function s6Resources(): { en: Tree; fa: Tree } {
  return {
    en: collect(import.meta.glob('./s6/*.en.json', { eager: true })),
    fa: collect(import.meta.glob('./s6/*.fa.json', { eager: true })),
  }
}
