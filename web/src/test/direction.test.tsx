import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { Ltr } from '@/components/ltr'
import { applyLocale, dirOf, intlLocale } from '@/i18n/direction'

describe('applyLocale', () => {
  it('fa sets rtl, lang and the Persian font', () => {
    applyLocale('fa')
    const root = document.documentElement
    expect(root.dir).toBe('rtl')
    expect(root.lang).toBe('fa')
    expect(root.classList.contains('font-fa')).toBe(true)
  })
  it('en sets ltr and removes the Persian font', () => {
    applyLocale('fa')
    applyLocale('en')
    const root = document.documentElement
    expect(root.dir).toBe('ltr')
    expect(root.lang).toBe('en')
    expect(root.classList.contains('font-fa')).toBe(false)
  })
  it('maps locales to direction and Intl locale', () => {
    expect(dirOf('fa')).toBe('rtl')
    expect(dirOf('en')).toBe('ltr')
    expect(intlLocale('fa')).toBe('fa-IR')
    expect(intlLocale('en')).toBe('en-US')
  })
})

describe('Ltr', () => {
  it('isolates content as left-to-right', () => {
    render(<Ltr>http://127.0.0.1:4280</Ltr>)
    expect(screen.getByText('http://127.0.0.1:4280')).toHaveAttribute('dir', 'ltr')
  })
})

describe('Radix direction', () => {
  it('Tabs follow the locale instead of stamping dir=ltr on themselves', async () => {
    const { Tabs, TabsList, TabsTrigger } = await import('@/components/ui/tabs')
    const { LocaleDirection } = await import('@/components/locale-direction')
    const view = (
      <LocaleDirection>
        <Tabs defaultValue="a">
          <TabsList>
            <TabsTrigger value="a">A</TabsTrigger>
          </TabsList>
        </Tabs>
      </LocaleDirection>
    )
    applyLocale('fa')
    const fa = render(view)
    expect(fa.container.querySelector('[data-slot="tabs"]')).toHaveAttribute('dir', 'rtl')
    fa.unmount()
    applyLocale('en')
    const en = render(view)
    expect(en.container.querySelector('[data-slot="tabs"]')).toHaveAttribute('dir', 'ltr')
    applyLocale('en')
  })
})
