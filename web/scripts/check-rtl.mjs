// Fails the build when source uses physical (left/right) Tailwind utilities.
// Use logical ones instead: ms-/me-/ps-/pe-/start-/end-/text-start/text-end/rounded-s/rounded-e/border-s/border-e.
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'

const ROOT = new URL('../src', import.meta.url).pathname
const PHYSICAL =
  /(?<![\w-])-?(?:ml|mr|pl|pr|left|right|rounded-l|rounded-r|rounded-tl|rounded-tr|rounded-bl|rounded-br|border-l|border-r|scroll-ml|scroll-mr|scroll-pl|scroll-pr)-(?:\d|\[|\(|px\b|auto\b|full\b|screen\b|none\b|xs\b|sm\b|md\b|lg\b|xl\b|2xl\b|3xl\b|4xl\b)/
const TEXT_SIDE = /(?<![\w-])(?:text-left|text-right|float-left|float-right|clear-left|clear-right)(?![\w-])/
// Variant prefixes such as `hover:` or `data-[x]:` are fine; they precede the utility.

function walk(dir) {
  const out = []
  for (const name of readdirSync(dir)) {
    const p = join(dir, name)
    if (statSync(p).isDirectory()) out.push(...walk(p))
    else if (p.endsWith('.tsx')) out.push(p)
  }
  return out
}

let failed = 0
for (const file of walk(ROOT)) {
  const lines = readFileSync(file, 'utf8').split('\n')
  lines.forEach((line, i) => {
    // only inspect string literals / template literals on the line
    const literals = line.match(/(["'`])(?:\\.|(?!\1).)*\1/g) ?? []
    for (const lit of literals) {
      for (const token of lit.slice(1, -1).split(/\s+/)) {
        const util = token.split(':').pop() ?? ''
        if (PHYSICAL.test(util) || TEXT_SIDE.test(util)) {
          console.error(`${file.replace(`${ROOT}/`, 'src/')}:${i + 1}: physical utility "${token}"`)
          failed++
        }
      }
    }
  })
}
// Radix roots default to dir="ltr" unless a DirectionProvider wraps the app (see components/locale-direction.tsx).
const main = readFileSync(join(ROOT, 'main.tsx'), 'utf8')
if (!main.includes('<LocaleDirection>')) {
  console.error('src/main.tsx: the app must be wrapped in <LocaleDirection> (Radix Tabs/Select otherwise force dir=ltr)')
  failed++
}
if (failed) {
  console.error(`check:rtl failed: ${failed} physical utilit${failed === 1 ? 'y' : 'ies'}`)
  process.exit(1)
}
console.log('check:rtl ok')
