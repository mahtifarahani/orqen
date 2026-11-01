import { useTranslation } from 'react-i18next'
import type { RunDto } from '@/api/generated/model'
import { useArtifactText } from '@/api/hooks/projects'
import { Ltr } from '@/components/ltr'
import { LoadFailed, LoadingBlock } from '@/components/query-state'
import { RiskBadge } from '@/components/run-state-badge'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'

interface Brief {
  goal: string
  acceptance: string[]
  test_contract: { key: string; kind: string; argv: string[] }[]
  risk: string
  risk_reasons: string[]
  protected_matches: string[]
  constraints: string[]
  out_of_scope: string[]
}

export function PlanTab({ orgId, run }: { orgId: string; run: RunDto | undefined }) {
  const { t } = useTranslation()
  const text = useArtifactText(orgId, run?.plan?.artifact_id)
  if (!run?.plan) return <p className="text-sm text-muted-foreground">{t('taskDetail.noPlan')}</p>
  if (text.isPending) return <LoadingBlock rows={2} />
  if (text.isError) return <LoadFailed title={t('taskDetail.planFailed')} error={text.error} onRetry={() => void text.refetch()} />
  let brief: Brief | null = null
  try {
    // The plan brief is JSON, which the fetch layer has already parsed; a string means it is still text.
    const raw: unknown = text.data
    brief = (typeof raw === 'string' ? JSON.parse(raw) : raw) as Brief
  } catch {
    brief = null
  }
  if (!brief) return <p className="text-sm text-destructive">{t('taskDetail.planFailed')}</p>
  return (
    <div className="grid gap-4 lg:grid-cols-2">
      <Card>
        <CardHeader>
          <CardTitle>{t('taskDetail.planGoal')}</CardTitle>
        </CardHeader>
        <CardContent className="flex flex-col gap-3 text-sm">
          <p dir="auto">{brief.goal}</p>
          <div className="flex flex-wrap items-center gap-2">
            <RiskBadge risk={brief.risk} />
            <Badge variant={run.plan.approved ? 'default' : 'outline'}>{run.plan.approved ? t('taskDetail.planApproved') : t('taskDetail.planNotApproved')}</Badge>
          </div>
          <ul className="list-disc ps-5">
            {brief.acceptance.map((a, i) => (
              <li key={i} dir="auto">
                {a}
              </li>
            ))}
          </ul>
        </CardContent>
      </Card>
      {(run.lanes?.length ?? 0) > 0 && (
        <Card className="lg:col-span-2">
          <CardHeader>
            <CardTitle>{t('taskDetail.planLanes')}</CardTitle>
          </CardHeader>
          <CardContent className="text-sm">
            <ul className="flex flex-col gap-2" aria-label={t('taskDetail.planLanes')}>
              {run.lanes.map((l) => (
                <li key={l.id}>
                  <Ltr className="font-mono">{l.id}</Ltr> <span dir="auto">{l.title}</span>
                  <span className="ms-2 text-muted-foreground">
                    <Ltr>{l.owns.join(', ')}</Ltr>
                  </span>
                </li>
              ))}
            </ul>
          </CardContent>
        </Card>
      )}
      <Card>
        <CardHeader>
          <CardTitle>{t('taskDetail.planTests')}</CardTitle>
        </CardHeader>
        <CardContent className="flex flex-col gap-2 text-sm">
          {brief.test_contract.length === 0 && <p className="text-muted-foreground">{t('taskDetail.planNoTests')}</p>}
          {brief.test_contract.map((c) => (
            <p key={c.key}>
              <strong>{c.key}</strong> · <Ltr>{c.argv.join(' ')}</Ltr>
            </p>
          ))}
          {brief.protected_matches.length > 0 && (
            <p>
              {t('taskDetail.planProtected')}: {brief.protected_matches.map((p) => <Ltr key={p} className="me-2">{p}</Ltr>)}
            </p>
          )}
          {brief.constraints.length > 0 && (
            <div>
              <p className="font-medium">{t('taskDetail.planConstraints')}</p>
              <ul className="list-disc ps-5">
                {brief.constraints.map((c, i) => (
                  <li key={i} dir="auto">
                    {c}
                  </li>
                ))}
              </ul>
            </div>
          )}
          {brief.out_of_scope.length > 0 && (
            <div>
              <p className="font-medium">{t('taskDetail.planOutOfScope')}</p>
              <ul className="list-disc ps-5 text-muted-foreground">
                {brief.out_of_scope.map((c, i) => (
                  <li key={i} dir="auto">
                    {c}
                  </li>
                ))}
              </ul>
            </div>
          )}
          <p className="text-xs text-muted-foreground">{t('taskDetail.planArchitect')}</p>
        </CardContent>
      </Card>
    </div>
  )
}
