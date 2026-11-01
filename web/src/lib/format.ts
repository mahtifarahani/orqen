import { intlLocale, type Locale } from '@/i18n/direction'

export function formatDateTime(iso: string | null | undefined, locale: Locale, timeZone?: string) {
  if (!iso) return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  try {
    return new Intl.DateTimeFormat(intlLocale(locale), { dateStyle: 'medium', timeStyle: 'short', timeZone }).format(d)
  } catch {
    return new Intl.DateTimeFormat(intlLocale(locale), { dateStyle: 'medium', timeStyle: 'short' }).format(d)
  }
}

export function formatTime(iso: string, locale: Locale) {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  return new Intl.DateTimeFormat(intlLocale(locale), { timeStyle: 'medium' }).format(d)
}

export function formatNumber(n: number, locale: Locale) {
  return new Intl.NumberFormat(intlLocale(locale)).format(n)
}
