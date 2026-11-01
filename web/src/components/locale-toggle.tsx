import { IconButton } from '@/components/icon-button'
import { LanguagesIcon } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { applyLocale, isLocale, storeLocale } from '@/i18n/direction'
import { useLocale } from '@/lib/use-locale'

export function LocaleToggle() {
  const { t } = useTranslation()
  const locale = useLocale()
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <IconButton variant="ghost" size="icon" aria-label={t('locale.label')}>
          <LanguagesIcon />
        </IconButton>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        <DropdownMenuRadioGroup
          value={locale}
          onValueChange={(v) => {
            if (!isLocale(v)) return
            storeLocale(v)
            applyLocale(v)
          }}
        >
          <DropdownMenuRadioItem value="fa" lang="fa">
            {t('locale.fa')}
          </DropdownMenuRadioItem>
          <DropdownMenuRadioItem value="en" lang="en">
            {t('locale.en')}
          </DropdownMenuRadioItem>
        </DropdownMenuRadioGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
