import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useParams } from 'react-router'
import { useExtensions } from '@/api/hooks/extensions'
import { Ltr } from '@/components/ltr'
import { LoadFailed, LoadingBlock } from '@/components/query-state'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { ExtensionSheet } from './extension-sheet'
import { stateVariant } from './state-variant'

/** Optional tools Orqen can use. Orqen never installs them; the person registers an existing binary. */
export function ExtensionsPage() {
  const { t } = useTranslation()
  const { orgId = '' } = useParams()
  const list = useExtensions(orgId)
  const [open, setOpen] = useState<string | null>(null)
  return (
    <div className="flex flex-col gap-6">
      <header>
        <h1 className="text-2xl font-semibold">{t('extensions.title')}</h1>
        <p className="text-sm text-muted-foreground">{t('extensions.subtitle')}</p>
      </header>
      {list.isPending && <LoadingBlock rows={4} />}
      {list.isError && <LoadFailed title={t('extensions.title')} error={list.error} onRetry={() => list.refetch()} />}
      {list.data && (
        <ul className="grid gap-3 md:grid-cols-2" aria-label={t('extensions.title')}>
          {list.data.map((e) => (
            <li key={e.manifest.id}>
              <Card>
                <CardContent className="flex flex-col gap-3">
                  <div className="flex flex-wrap items-start justify-between gap-2">
                    <div className="min-w-0">
                      <h2 className="font-medium">{t(`extensions.names.${e.manifest.id}`, { defaultValue: e.manifest.id })}</h2>
                      <Ltr className="text-xs text-muted-foreground">{e.manifest.id}</Ltr>
                    </div>
                    <Badge variant={stateVariant(e.state)} data-ext-state={e.state}>
                      {t(`extensions.state.${e.state}`, { defaultValue: e.state })}
                    </Badge>
                  </div>
                  <div className="flex flex-wrap gap-2">
                    <Badge variant="outline">{t(`extensions.kind.${e.manifest.kind}`)}</Badge>
                    <Badge variant="outline">{t(`extensions.integration.${e.manifest.integration}`)}</Badge>
                    {e.health && <Badge variant={e.health.status === 'ok' ? 'secondary' : 'destructive'}>{t(`extensions.healthStatus.${e.health.status}`)}</Badge>}
                  </div>
                  {!e.usable.ok && e.usable.reason && <p className="text-sm text-muted-foreground">{t(`extensions.unusable.${e.usable.reason}`, { defaultValue: e.usable.reason })}</p>}
                  <div>
                    <Button variant="outline" size="sm" onClick={() => setOpen(e.manifest.id)}>
                      {t('extensions.manage')}
                    </Button>
                  </div>
                </CardContent>
              </Card>
            </li>
          ))}
        </ul>
      )}
      <ExtensionSheet orgId={orgId} extId={open} onClose={() => setOpen(null)} />
    </div>
  )
}
