import { useTranslation } from 'react-i18next'
import type { RunDto } from '@/api/generated/model'
import { useRunVerification } from '@/api/hooks/runDetail'
import { Ltr } from '@/components/ltr'
import { LoadFailed, LoadingBlock } from '@/components/query-state'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'

/** Verification evidence of each round: commands, outcomes, test-weakening signals, the gate and any waiver. */
export function VerificationTab({ orgId, run }: { orgId: string; run: RunDto | undefined }) {
  const { t } = useTranslation()
  const list = useRunVerification(orgId, run?.id)
  if (!run) return <p className="text-sm text-muted-foreground">{t('taskDetail.noRun')}</p>
  if (list.isPending) return <LoadingBlock rows={2} />
  if (list.isError) return <LoadFailed title={t('verification.loadFailed')} error={list.error} onRetry={() => void list.refetch()} />
  const heads = list.data.filter((e) => e.kind === 'head')
  if (heads.length === 0) return <p className="text-sm text-muted-foreground">{t('verification.none')}</p>
  return (
    <div className="flex flex-col gap-4">
      {[...heads].reverse().map((e) => (
        <Card key={e.id}>
          <CardHeader>
            <CardTitle className="flex flex-wrap items-center gap-2">
              {t('verification.round', { n: e.round })}
              <Badge variant="outline" data-gate={e.gate.status}>
                {t(`verification.gate.${e.gate.status}`, { defaultValue: e.gate.status })}
              </Badge>
              <Ltr className="font-mono text-xs text-muted-foreground">{e.head_sha.slice(0, 12)}</Ltr>
            </CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col gap-3 text-sm">
            {e.gate.reasons.length > 0 && (
              <ul className="list-disc ps-5 text-muted-foreground">
                {e.gate.reasons.map((r) => {
                  const [code = r, ...rest] = r.split(':')
                  return (
                    <li key={r}>
                      {t(`verification.reasons.${code}`, { defaultValue: code })} <Ltr>{rest.join(' ')}</Ltr>
                    </li>
                  )
                })}
              </ul>
            )}
            <ul className="flex flex-col gap-1" aria-label={t('verification.commands')}>
              {e.commands.map((c) => (
                <li key={c.key} className="flex flex-wrap items-center gap-2">
                  <strong>{c.key}</strong>
                  <Badge variant="outline" data-outcome={c.outcome}>
                    {t(`verification.outcome.${c.outcome}`, { defaultValue: c.outcome })}
                  </Badge>
                  <Ltr className="font-mono text-xs">{c.argv.join(' ')}</Ltr>
                  <span className="text-xs text-muted-foreground">
                    {t(`verification.sandbox.${c.sandbox}`, { defaultValue: c.sandbox })}
                  </span>
                </li>
              ))}
            </ul>
            {e.weakening.suspected && (
              <Alert variant="destructive">
                <AlertTitle>{t('verification.weakening')}</AlertTitle>
                <AlertDescription>
                  <ul className="list-disc ps-5">
                    {e.weakening.signals.map((s, i) => (
                      <li key={i}>
                        {t(`verification.signals.${s.code}`, { defaultValue: s.code })} <Ltr>{s.path}</Ltr>
                      </li>
                    ))}
                  </ul>
                </AlertDescription>
              </Alert>
            )}
            {e.verifier && (
              <p>
                {t('verification.verifier')}: <Ltr>{e.verifier.verdict}</Ltr>
              </p>
            )}
            {e.waiver && (
              <p role="status">
                {t('verification.waived')}: <span dir="auto">{e.waiver.text}</span>
              </p>
            )}
          </CardContent>
        </Card>
      ))}
    </div>
  )
}
