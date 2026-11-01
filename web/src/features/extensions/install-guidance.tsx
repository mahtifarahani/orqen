import { useTranslation } from 'react-i18next'
import { Ltr } from '@/components/ltr'
import type { ExtensionDetail } from '@/api/hooks/extensions'

/** What the person has to do outside Orqen: Orqen never downloads or installs an extension. */
export function InstallGuidance({ detail }: { detail: ExtensionDetail }) {
  const { t } = useTranslation()
  const g = detail.install_guidance
  return (
    <section className="flex flex-col gap-2" aria-label={t('extensions.guidance.title')}>
      <h3 className="font-medium">{t('extensions.guidance.title')}</h3>
      <p className="text-sm text-muted-foreground">{t(g.text_key, { defaultValue: t('extensions.guidance.generic') })}</p>
      {g.commands.length > 0 && (
        <ul className="flex flex-col gap-1">
          {g.commands.map((c) => (
            <li key={c}>
              <Ltr className="block overflow-x-auto rounded-md bg-muted px-2 py-1 text-xs">{c}</Ltr>
            </li>
          ))}
        </ul>
      )}
      <p className="text-xs text-muted-foreground">{t('extensions.guidance.noDownload')}</p>
    </section>
  )
}
