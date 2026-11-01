import i18n from './index'

export type Locale = 'fa' | 'en'
export const LOCALE_KEY = 'orqen.locale'

export const isLocale = (x: unknown): x is Locale => x === 'fa' || x === 'en'
export const dirOf = (locale: Locale) => (locale === 'fa' ? 'rtl' : 'ltr')

export function storedLocale(): Locale | null {
  try {
    const v = localStorage.getItem(LOCALE_KEY)
    return isLocale(v) ? v : null
  } catch {
    return null
  }
}

export function storeLocale(locale: Locale) {
  try {
    localStorage.setItem(LOCALE_KEY, locale)
  } catch {
    /* storage unavailable: the choice just won't persist */
  }
}

/** Sets <html lang dir>, toggles the Persian font and switches the i18n language. */
export function applyLocale(locale: Locale) {
  const root = document.documentElement
  root.lang = locale
  root.dir = dirOf(locale)
  root.classList.toggle('font-fa', locale === 'fa')
  void i18n.changeLanguage(locale)
}

/** Number/date formatting locale (D28). */
export const intlLocale = (locale: Locale) => (locale === 'fa' ? 'fa-IR' : 'en-US')
