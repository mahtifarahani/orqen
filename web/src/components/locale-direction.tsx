import type { ReactNode } from 'react'
import { Direction } from 'radix-ui'
import { dirOf } from '@/i18n/direction'
import { useLocale } from '@/lib/use-locale'

/**
 * Radix primitives (Tabs, Select, Switch, menus, sliders) do not read `<html dir>`: without a provider
 * they stamp `dir="ltr"` on their own root, which silently flips every container inside them to LTR
 * flow in Persian. Mounting this once at the app root makes them follow the active locale.
 */
export function LocaleDirection({ children }: { children: ReactNode }) {
  return <Direction.Provider dir={dirOf(useLocale())}>{children}</Direction.Provider>
}
