import { useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Link, useParams } from 'react-router'
import { ArchiveIcon, FlaskConicalIcon, KeyRoundIcon, PencilIcon, PlusIcon, TelescopeIcon } from 'lucide-react'
import { useConnections, type Connection } from '@/api/hooks/connections'
import { useOrg } from '@/api/hooks/orgs'
import { Ltr } from '@/components/ltr'
import { StatusBadge } from '@/components/status-badge'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { formatDateTime } from '@/lib/format'
import { problemMessage } from '@/lib/problem-message'
import { useLocale } from '@/lib/use-locale'
import { ArchiveDialog } from './archive-dialog'
import { ConnectionFormDialog } from './connection-form-dialog'
import { ConnectionTestPanel } from './connection-test-panel'
import { CredentialDialog } from './credential-dialog'
import { ReadinessBadge } from './readiness-badge'

type Action = { kind: 'edit' | 'credential' | 'test' | 'archive'; id: string }

export function ConnectionsPage({ embedded = false, onDone }: { embedded?: boolean; onDone?: () => void } = {}) {
  const { t } = useTranslation()
  const locale = useLocale()
  const { orgId = '' } = useParams()
  const org = useOrg(orgId)
  const list = useConnections(orgId)
  const [createOpen, setCreateOpen] = useState(false)
  const [action, setAction] = useState<Action | null>(null)
  // The element that opened a dialog gets focus back when it closes.
  const addRef = useRef<HTMLButtonElement>(null)
  const returnRef = useRef<HTMLElement | null>(null)

  const open = (kind: Action['kind'], c: Connection, el: HTMLElement) => {
    returnRef.current = el
    setAction({ kind, id: c.id })
  }
  const current = action ? list.data?.find((c) => c.id === action.id) : undefined
  const close = (kind: Action['kind']) => (o: boolean) => {
    if (!o && action?.kind === kind) setAction(null)
  }

  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-wrap items-start justify-between gap-4">
        {embedded ? (
          <p className="max-w-prose text-sm text-muted-foreground">{t('connections.subtitle')}</p>
        ) : (
          <div>
            <h1 className="text-2xl font-semibold">{t('connections.title')}</h1>
            <p className="text-sm text-muted-foreground">{t('connections.subtitle')}</p>
          </div>
        )}
        <Button ref={addRef} onClick={() => setCreateOpen(true)}>
          <PlusIcon />
          {t('connections.add')}
        </Button>
      </div>

      {list.isPending && (
        <div className="flex flex-col gap-2" role="status" aria-label={t('app.loading')}>
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
        </div>
      )}
      {list.isError && (
        <Alert variant="destructive">
          <AlertTitle>{t('connections.loadFailed')}</AlertTitle>
          <AlertDescription className="flex flex-col items-start gap-2">
            {problemMessage(t, list.error)}
            <Button size="sm" variant="outline" onClick={() => void list.refetch()}>
              {t('common.retry')}
            </Button>
          </AlertDescription>
        </Alert>
      )}
      {list.data?.length === 0 && (
        <Card>
          <CardContent className="flex flex-col items-start gap-4 py-6">
            <p className="max-w-prose text-sm text-muted-foreground">{t('connections.empty')}</p>
            <Button onClick={() => setCreateOpen(true)}>
              <PlusIcon />
              {t('connections.add')}
            </Button>
          </CardContent>
        </Card>
      )}
      {list.data && list.data.length > 0 && (
        <div className="overflow-x-auto rounded-lg border">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t('connections.columns.name')} / {t('connections.columns.provider')}</TableHead>
                <TableHead>{t('connections.columns.baseUrl')}</TableHead>
                <TableHead>{t('connections.columns.credential')}</TableHead>
                <TableHead>{t('connections.columns.status')}</TableHead>
                <TableHead>{t('connections.columns.lastTested')}</TableHead>
                <TableHead>{t('connections.columns.readiness')}</TableHead>
                <TableHead>
                  <span className="sr-only">{t('common.actions')}</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {list.data.map((c) => (
                <TableRow key={c.id}>
                  <TableCell>
                    <div className="font-medium">{c.name}</div>
                    <div className="text-xs text-muted-foreground">{t(`providers.${c.provider}`)}</div>
                  </TableCell>
                  <TableCell className="max-w-44 whitespace-normal break-all">
                    <Ltr className="text-xs">{c.base_url}</Ltr>
                  </TableCell>
                  <TableCell>
                    <Badge variant={c.credential.state === 'set' ? 'secondary' : 'outline'}>
                      {c.credential.state === 'set' ? t('connections.credential.set') : t('connections.credential.none')}
                    </Badge>
                  </TableCell>
                  <TableCell>
                    <StatusBadge status={c.status} />
                  </TableCell>
                  <TableCell className="whitespace-normal text-xs text-muted-foreground">
                    {c.last_test ? formatDateTime(c.last_test.finished_at, locale, org.data?.timezone) : t('common.never')}
                  </TableCell>
                  <TableCell>
                    <ReadinessBadge readiness={c.readiness} />
                  </TableCell>
                  <TableCell>
                    <div className="flex items-center justify-end gap-1">
                      <Tooltip>
                        <TooltipTrigger asChild>
                          <Button variant="ghost" size="icon-sm" aria-label={t('connections.actions.editLabel', { name: c.name })} onClick={(e) => open('edit', c, e.currentTarget)}>
                            <PencilIcon />
                          </Button>
                        </TooltipTrigger>
                        <TooltipContent sideOffset={6}>{t('connections.actions.editLabel', { name: c.name })}</TooltipContent>
                      </Tooltip>
                      <Tooltip>
                        <TooltipTrigger asChild>
                          <Button variant="ghost" size="icon-sm" aria-label={t('connections.actions.credentialLabel', { name: c.name })} onClick={(e) => open('credential', c, e.currentTarget)}>
                            <KeyRoundIcon />
                          </Button>
                        </TooltipTrigger>
                        <TooltipContent sideOffset={6}>{t('connections.actions.credentialLabel', { name: c.name })}</TooltipContent>
                      </Tooltip>
                      <Tooltip>
                        <TooltipTrigger asChild>
                          <Button variant="ghost" size="icon-sm" aria-label={t('connections.actions.testLabel', { name: c.name })} onClick={(e) => open('test', c, e.currentTarget)}>
                            <FlaskConicalIcon />
                          </Button>
                        </TooltipTrigger>
                        <TooltipContent sideOffset={6}>{t('connections.actions.testLabel', { name: c.name })}</TooltipContent>
                      </Tooltip>
                      {c.provider === 'gitlab' && (
                        <Tooltip>
                          <TooltipTrigger asChild>
                            <Button asChild variant="ghost" size="icon-sm">
                              <Link to={`/orgs/${orgId}/projects/discover`} aria-label={t('connections.actions.discoverLabel', { name: c.name })}>
                                <TelescopeIcon />
                              </Link>
                            </Button>
                          </TooltipTrigger>
                          <TooltipContent sideOffset={6}>{t('connections.actions.discoverLabel', { name: c.name })}</TooltipContent>
                        </Tooltip>
                      )}
                      <Tooltip>
                        <TooltipTrigger asChild>
                          <Button variant="ghost" size="icon-sm" aria-label={t('connections.actions.archiveLabel', { name: c.name })} onClick={(e) => open('archive', c, e.currentTarget)}>
                            <ArchiveIcon />
                          </Button>
                        </TooltipTrigger>
                        <TooltipContent sideOffset={6}>{t('connections.actions.archiveLabel', { name: c.name })}</TooltipContent>
                      </Tooltip>
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}

      {embedded && onDone && (
        <div>
          <Button variant="outline" onClick={onDone}>
            {t('connections.done')}
          </Button>
        </div>
      )}
      <ConnectionFormDialog orgId={orgId} open={createOpen} onOpenChange={setCreateOpen} returnFocusRef={addRef} />
      {current && action?.kind === 'edit' && (
        <ConnectionFormDialog orgId={orgId} connection={current} open onOpenChange={close('edit')} returnFocusRef={returnRef} />
      )}
      {current && action?.kind === 'credential' && (
        <CredentialDialog orgId={orgId} connection={current} open onOpenChange={close('credential')} returnFocusRef={returnRef} />
      )}
      {current && action?.kind === 'test' && (
        <ConnectionTestPanel orgId={orgId} connection={current} open onOpenChange={close('test')} returnFocusRef={returnRef} />
      )}
      {current && action?.kind === 'archive' && (
        <ArchiveDialog orgId={orgId} connection={current} open onOpenChange={close('archive')} returnFocusRef={returnRef} />
      )}
    </div>
  )
}
