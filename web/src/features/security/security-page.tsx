import { useTranslation } from 'react-i18next'
import { Link, useParams } from 'react-router'
import { useSecurityScans, useSecurityStatus } from '@/api/hooks/security'
import { LoadFailed, LoadingBlock } from '@/components/query-state'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent } from '@/components/ui/card'
import { ScanList } from './scan-list'

/** Org wide security: whether scanning is usable, the policy it runs under, open blocking findings and recent scans. */
export function SecurityPage() {
  const { t } = useTranslation()
  const { orgId = '' } = useParams()
  const status = useSecurityStatus(orgId)
  const scans = useSecurityScans(orgId)
  const s = status.data
  return (
    <div className="flex flex-col gap-6">
      <header>
        <h1 className="text-2xl font-semibold">{t('security.title')}</h1>
        <p className="text-sm text-muted-foreground">{t('security.subtitle')}</p>
      </header>
      {status.isPending && <LoadingBlock rows={2} />}
      {status.isError && <LoadFailed title={t('security.title')} error={status.error} onRetry={() => status.refetch()} />}
      {s && (
        <Card>
          <CardContent className="flex flex-col gap-3">
            <div className="flex flex-wrap items-center gap-2" role="status">
              <Badge variant={s.usable.ok ? 'default' : 'destructive'} data-security-usable={s.usable.ok}>
                {t(s.usable.ok ? 'security.status.usable' : 'security.status.unusable')}
              </Badge>
              {!s.usable.ok && s.usable.reason && <span className="text-sm text-muted-foreground">{t(`extensions.unusable.${s.usable.reason}`, { defaultValue: t(`problems.${s.usable.reason}`, { defaultValue: s.usable.reason }) })}</span>}
            </div>
            <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm">
              <dt className="text-muted-foreground">{t('security.policy.scan')}</dt>
              <dd>{s.policy.scan ? t(`policy.values.${s.policy.scan}`, { defaultValue: s.policy.scan }) : '-'}</dd>
              <dt className="text-muted-foreground">{t('security.policy.gate')}</dt>
              <dd>{s.policy.gate ? t(`policy.values.${s.policy.gate}`, { defaultValue: s.policy.gate }) : '-'}</dd>
              <dt className="text-muted-foreground">{t('security.policy.severity')}</dt>
              <dd>{s.policy.blocking_severity ? t(`policy.values.${s.policy.blocking_severity}`, { defaultValue: s.policy.blocking_severity }) : '-'}</dd>
              <dt className="text-muted-foreground">{t('security.policy.model')}</dt>
              <dd>{t(s.policy.model_set ? 'security.policy.modelSet' : 'security.policy.modelUnset')}</dd>
              <dt className="text-muted-foreground">{t('security.policy.budget')}</dt>
              <dd>{s.policy.budget_mode ? t(`security.budgetMode.${s.policy.budget_mode}`, { defaultValue: s.policy.budget_mode }) : '-'}</dd>
              <dt className="text-muted-foreground">{t('security.counts.running')}</dt>
              <dd>{s.counts.scans_running ?? 0}</dd>
              <dt className="text-muted-foreground">{t('security.counts.openBlocking')}</dt>
              <dd>{s.counts.open_blocking ?? 0}</dd>
            </dl>
            <p className="text-sm text-muted-foreground">
              {t('security.policy.where')} <Link className="underline underline-offset-4" to={`/orgs/${orgId}/settings`}>{t('security.policy.settingsLink')}</Link> · <Link className="underline underline-offset-4" to={`/orgs/${orgId}/extensions`}>{t('nav.extensions')}</Link>
            </p>
          </CardContent>
        </Card>
      )}
      <section className="flex flex-col gap-2" aria-label={t('security.scans.title')}>
        <h2 className="text-lg font-semibold">{t('security.scans.title')}</h2>
        {scans.isPending && <LoadingBlock rows={2} />}
        {scans.isError && <LoadFailed title={t('security.scans.title')} error={scans.error} onRetry={() => scans.refetch()} />}
        {scans.data && <ScanList orgId={orgId} scans={scans.data} />}
      </section>
    </div>
  )
}
