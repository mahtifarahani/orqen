import { useTranslation } from 'react-i18next'
import type { Locale } from '@/i18n/direction'

export function useLocale(): Locale {
  const { i18n } = useTranslation()
  return i18n.language === 'fa' ? 'fa' : 'en'
}
