import { useTranslation } from 'react-i18next'
import type { RunDto } from '@/api/generated/model'
import { useRunRoles } from '@/api/hooks/runDetail'
import { RunSkillsManifest } from '@/features/skills/run-skills-manifest'
import { Ltr } from '@/components/ltr'
import { LoadFailed, LoadingBlock } from '@/components/query-state'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent } from '@/components/ui/card'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { formatNumber } from '@/lib/format'
import { useLocale } from '@/lib/use-locale'

/** The roles catherd ran for this run, with the Codex host session that dispatched them. */
export function RolesTab({ orgId, run }: { orgId: string; run: RunDto | undefined }) {
  const { t } = useTranslation()
  const locale = useLocale()
  const roles = useRunRoles(orgId, run?.id)
  if (!run) return <p className="text-sm text-muted-foreground">{t('taskDetail.noRun')}</p>
  if (roles.isPending) return <LoadingBlock rows={2} />
  if (roles.isError) return <LoadFailed title={t('roles.loadFailed')} error={roles.error} onRetry={() => void roles.refetch()} />
  const { host_session: hs, roles: list } = roles.data
  return (
    <div className="flex flex-col gap-4">
      {hs && (
        <Card>
          <CardContent className="flex flex-wrap gap-x-6 gap-y-1 p-4 text-sm">
            <span className="font-medium">{t('roles.hostSession')}</span>
            <span>
              {t('roles.model')}: <Ltr>{hs.model || '-'}</Ltr>
            </span>
            <span>
              {t('roles.codex')}: <Ltr>{hs.codex_version}</Ltr>
            </span>
            <span>
              {t('roles.thread')}: <Ltr>{hs.thread_id.slice(0, 12)}</Ltr>
            </span>
            {hs.host_tokens != null && <span>{t('roles.hostTokens', { n: formatNumber(hs.host_tokens, locale) })}</span>}
          </CardContent>
        </Card>
      )}
      {list.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t('roles.none')}</p>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t('roles.role')}</TableHead>
              <TableHead>{t('roles.state')}</TableHead>
              <TableHead>{t('roles.rung')}</TableHead>
              <TableHead>{t('roles.round')}</TableHead>
              <TableHead>{t('roles.reply')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {list.map((r) => (
              <TableRow key={r.id}>
                <TableCell>
                  <Ltr>{r.name}</Ltr>
                  {r.lane && <span className="ms-2 text-muted-foreground"><Ltr>{r.lane}</Ltr></span>}
                </TableCell>
                <TableCell>
                  <Badge variant="outline" data-role-state={r.state}>
                    {t(`roles.states.${r.state}`, { defaultValue: r.state })}
                  </Badge>
                </TableCell>
                <TableCell>
                  <Ltr>{r.catherd?.rung || r.requested_rung}</Ltr>
                  {r.catherd?.failover_from && <span className="ms-2 text-xs text-muted-foreground">{t('roles.failover')}</span>}
                </TableCell>
                <TableCell>{r.round}</TableCell>
                <TableCell>{r.catherd?.reply_status ? <Ltr>{r.catherd.reply_status}</Ltr> : '-'}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
      <RunSkillsManifest orgId={orgId} runId={run.id} />
    </div>
  )
}
