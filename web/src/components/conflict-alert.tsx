import { TriangleAlertIcon } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Ltr } from '@/components/ltr'

export interface ConflictRow {
  label: string
  mine: string
  latest: string
}

/** Shown on 409 revision_conflict. The user's input stays visible until they pick an action. */
export function ConflictAlert({
  rows,
  onLoadLatest,
  onKeepMine,
}: {
  rows: ConflictRow[]
  onLoadLatest: () => void
  onKeepMine: () => void
}) {
  const { t } = useTranslation()
  return (
    <Alert variant="destructive" role="alert" data-testid="conflict-alert">
      <TriangleAlertIcon />
      <AlertTitle>{t('conflict.title')}</AlertTitle>
      <AlertDescription className="flex flex-col gap-3">
        <p>{t('conflict.body')}</p>
        {rows.length > 0 && (
          <dl className="grid grid-cols-[auto_1fr_1fr] gap-x-4 gap-y-1 text-xs">
            <dt className="font-medium" />
            <dd className="font-medium">{t('conflict.yourValue')}</dd>
            <dd className="font-medium">{t('conflict.latestValue')}</dd>
            {rows.map((r) => (
              <div key={r.label} className="col-span-3 grid grid-cols-subgrid">
                <dt>{r.label}</dt>
                <dd className="break-all">
                  <bdi>{r.mine || '—'}</bdi>
                </dd>
                <dd className="break-all">
                  <Ltr className="font-sans">{r.latest || '—'}</Ltr>
                </dd>
              </div>
            ))}
          </dl>
        )}
        <div className="flex flex-wrap gap-2">
          <Button type="button" size="sm" variant="outline" onClick={onLoadLatest}>
            {t('conflict.loadLatest')}
          </Button>
          <Button type="button" size="sm" variant="outline" onClick={onKeepMine}>
            {t('conflict.keepMine')}
          </Button>
        </div>
      </AlertDescription>
    </Alert>
  )
}
