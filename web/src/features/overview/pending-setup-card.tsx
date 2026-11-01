import { useTranslation } from 'react-i18next'
import { Link, useParams } from 'react-router'
import { TriangleAlertIcon } from 'lucide-react'
import { useSetup } from '@/api/hooks/setup'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'

/** Lists the setup steps that are still todo, skipped or need attention, with what each one costs. Hidden when nothing is pending. */
export function PendingSetupCard() {
  const { t } = useTranslation()
  const { orgId = '' } = useParams()
  const setup = useSetup(orgId)
  if (!setup.data || setup.data.pending_count === 0) return null
  const pending = setup.data.steps.filter((s) => s.status !== 'done' || s.attention)
  return (
    <Card aria-labelledby="pending-setup-title">
      <CardHeader>
        <CardTitle id="pending-setup-title" className="flex items-center gap-2">
          <TriangleAlertIcon className="size-4" aria-hidden />
          {t('overview.pendingSetup.title')}
        </CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <p className="text-sm text-muted-foreground">{t('overview.pendingSetup.body', { count: setup.data.pending_count })}</p>
        <ul className="flex flex-col divide-y">
          {pending.map((s) => (
            <li key={s.key} className="flex flex-wrap items-start justify-between gap-2 py-2 text-sm">
              <div className="flex flex-col gap-0.5">
                <span className="font-medium">
                  {t(`wizard.steps.${s.key}.title`)} · <span className="font-normal text-muted-foreground">{t(`wizard.status.${s.attention ? 'attention' : s.status}`)}</span>
                </span>
                {s.consequences.length > 0 && (
                  <ul className="list-disc ps-5 text-xs text-muted-foreground">
                    {s.consequences.map((c) => (
                      <li key={c}>{t(`wizard.consequence.${c}`, { defaultValue: c })}</li>
                    ))}
                  </ul>
                )}
              </div>
              <Button asChild size="sm" variant="outline">
                <Link to={`/orgs/${orgId}/setup?step=${s.key}`}>
                  {t('wizard.actions.resume')}
                  <span className="sr-only"> {t(`wizard.steps.${s.key}.title`)}</span>
                </Link>
              </Button>
            </li>
          ))}
        </ul>
      </CardContent>
    </Card>
  )
}
