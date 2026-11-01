import { useTranslation } from 'react-i18next'
import { Link } from 'react-router'
import type { CatalogProject } from '@/api/hooks/catalog'
import { Ltr } from '@/components/ltr'
import { Badge } from '@/components/ui/badge'
import { Checkbox } from '@/components/ui/checkbox'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { formatDateTime } from '@/lib/format'
import { useLocale } from '@/lib/use-locale'
import { selectable } from './catalog-selection'

export function CatalogTable({
  orgId,
  rows,
  selected,
  onToggle,
  onToggleAll,
  timeZone,
}: {
  orgId: string
  rows: CatalogProject[]
  selected: ReadonlySet<string>
  onToggle: (providerProjectId: string) => void
  onToggleAll: (providerProjectIds: string[], checked: boolean) => void
  timeZone?: string
}) {
  const { t } = useTranslation()
  const locale = useLocale()
  const ids = rows.filter(selectable).map((p) => p.provider_project_id)
  const chosen = ids.filter((id) => selected.has(id)).length
  const all: boolean | 'indeterminate' = ids.length > 0 && chosen === ids.length ? true : chosen > 0 ? 'indeterminate' : false

  return (
    <div className="overflow-x-auto rounded-lg border">
      <Table aria-label={t('discover.title')}>
        <TableHeader>
          <TableRow>
            <TableHead className="w-10">
              <Checkbox checked={all} disabled={ids.length === 0} aria-label={t('discover.selectAll')} onCheckedChange={(v) => onToggleAll(ids, v === true)} />
            </TableHead>
            <TableHead>{t('discover.columns.name')}</TableHead>
            <TableHead>{t('discover.columns.path')}</TableHead>
            <TableHead>{t('discover.columns.branch')}</TableHead>
            <TableHead>{t('discover.columns.visibility')}</TableHead>
            <TableHead>{t('discover.columns.activity')}</TableHead>
            <TableHead>{t('discover.columns.status')}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.map((p) => (
            <TableRow key={p.id} data-state={selected.has(p.provider_project_id) ? 'selected' : undefined}>
              <TableCell>
                <Checkbox
                  checked={selected.has(p.provider_project_id)}
                  disabled={!selectable(p)}
                  aria-label={t('discover.selectRow', { name: p.name })}
                  onCheckedChange={() => onToggle(p.provider_project_id)}
                />
              </TableCell>
              <TableCell className="font-medium">{p.name}</TableCell>
              <TableCell className="whitespace-normal break-all text-xs">
                <Ltr>{p.path_with_namespace}</Ltr>
              </TableCell>
              <TableCell className="text-xs">{p.default_branch ? <Ltr>{p.default_branch}</Ltr> : t('common.none')}</TableCell>
              <TableCell className="text-xs text-muted-foreground">{p.visibility ?? t('common.none')}</TableCell>
              <TableCell className="whitespace-normal text-xs text-muted-foreground">{formatDateTime(p.last_activity_at, locale, timeZone)}</TableCell>
              <TableCell>
                <div className="flex flex-wrap gap-1">
                  {p.project_id && (
                    <Badge asChild variant="secondary">
                      <Link to={`/orgs/${orgId}/projects/${p.project_id}`}>{t('discover.configured')}</Link>
                    </Badge>
                  )}
                  {p.archived && <Badge variant="outline">{t('discover.archived')}</Badge>}
                  {p.gone && <Badge variant="outline">{t('discover.gone')}</Badge>}
                </div>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  )
}
