import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Link, useParams } from 'react-router'
import { useQueryClient } from '@tanstack/react-query'
import { ArrowLeftIcon, GitCompareIcon, RotateCcwIcon } from 'lucide-react'
import { toast } from 'sonner'
import type { CatherdIssue, CatherdProfileDoc, ModelProfileDto, ModelProfileVersionDto, ProfileMeta } from '@/api/generated/model'
import { useCatherdSchema, useCatherdStatus } from '@/api/hooks/catherd'
import {
  useDraftIssuesQuery,
  useProfile,
  useProfileDiffQuery,
  useProfileVersion,
  useProfileVersions,
  usePublishProfile,
  usePutProfileDraft,
  useRollbackProfile,
  useStartValidation,
} from '@/api/hooks/models'
import { invalidatePath } from '@/api/query-keys'
import { ok } from '@/api/problem'
import { ConflictAlert } from '@/components/conflict-alert'
import { Ltr } from '@/components/ltr'
import { LoadFailed, LoadingBlock } from '@/components/query-state'
import { useOperationRunner } from '@/components/use-operation-runner'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { conflictCurrent, sameJson } from '@/lib/conflict'
import { formatDateTime } from '@/lib/format'
import { problemMessage } from '@/lib/problem-message'
import { useLocale } from '@/lib/use-locale'
import { IndependenceBadge } from './independence-badge'
import { AccessSection, AdvancedSection, BillingSection, LifecycleSection, RolesSection, RoutingSection, type SectionProps } from './profile-sections'

const EDITOR_TABS = ['roles', 'routing', 'access', 'billing', 'lifecycle', 'advanced'] as const

interface Props {
  embedded?: boolean
  profileId?: string
  onDone?: () => void
}

export function ProfileEditorPage({ embedded = false, profileId: idProp, onDone }: Props) {
  const { t } = useTranslation()
  const params = useParams()
  const orgId = params.orgId ?? ''
  const profileId = idProp ?? params.profileId ?? ''
  const profile = useProfile(orgId, profileId)
  return (
    <div className="flex flex-col gap-6">
      {!embedded && (
        <Button asChild variant="ghost" className="self-start">
          <Link to={`/orgs/${orgId}/models`}>
            <ArrowLeftIcon className="rtl:-scale-x-100" />
            {t('profile.back')}
          </Link>
        </Button>
      )}
      {profile.isPending && <LoadingBlock />}
      {profile.isError && <LoadFailed title={t('profile.loadFailed')} error={profile.error} onRetry={() => void profile.refetch()} />}
      {profile.data && <Editor key={profile.data.id} orgId={orgId} profile={profile.data} embedded={embedded} onDone={onDone} />}
    </div>
  )
}

const toBody = (p: ModelProfileDto) => ({ doc: p.draft.doc, meta: p.draft.meta })

function Editor({ orgId, profile, embedded, onDone }: { orgId: string; profile: ModelProfileDto; embedded: boolean; onDone?: () => void }) {
  const { t } = useTranslation()
  const locale = useLocale()
  const qc = useQueryClient()
  const status = useCatherdStatus(orgId)
  const schema = useCatherdSchema(orgId)
  const [base, setBase] = useState(profile)
  const [doc, setDoc] = useState<CatherdProfileDoc>(profile.draft.doc)
  const [meta, setMeta] = useState<ProfileMeta>(profile.draft.meta)
  const [conflict, setConflict] = useState<ModelProfileDto | null>(null)
  const [jsonError, setJsonError] = useState<string | null>(null)
  const [diffOpen, setDiffOpen] = useState<string | null>(null)
  const [rollback, setRollback] = useState<ModelProfileVersionDto | null>(null)
  // A version built for another orchestrator host than the one in use: rolling back to it republishes it for the current host.
  const staleHost = (v: ModelProfileVersionDto) => !!status.data?.host && v.host !== status.data.host
  const dirty = !sameJson({ doc, meta }, toBody(base))
  if (!dirty && profile.revision > base.revision) {
    setBase(profile)
    setDoc(profile.draft.doc)
    setMeta(profile.draft.meta)
  }
  const issues = useDraftIssuesQuery(orgId, base.id, profile.draft_hash)
  const versions = useProfileVersions(orgId, base.id)
  const latest = useProfileVersion(orgId, profile.latest_version_id)
  const save = usePutProfileDraft(orgId, base.revision)

  const validateRunner = useOperationRunner(orgId)
  const validate = useStartValidation(validateRunner.key)
  const publishRunner = useOperationRunner(orgId)
  const publish = usePublishProfile(base.revision, publishRunner.key)
  const rollbackRunner = useOperationRunner(orgId)
  const rollbackM = useRollbackProfile(base.revision, rollbackRunner.key)

  // When an operation finishes, re-read the profile and its versions (SSE does this too; this is the safety net).
  const finished = [validateRunner, publishRunner, rollbackRunner].map((r) => r.operation?.status).join()
  useEffect(() => {
    if (/succeeded|failed/.test(finished)) invalidatePath(qc, (p) => p.startsWith(`/api/v1/orgs/${orgId}/model-profile`))
  }, [finished, qc, orgId])
  useEffect(() => {
    if (publishRunner.succeeded) toast.success(t('profile.publish.done'))
    if (rollbackRunner.succeeded) toast.success(t('profile.versions.rolledBack'))
  }, [publishRunner.succeeded, rollbackRunner.succeeded, t])

  const degraded = status.isSuccess && (!status.data.available || !status.data.supported)
  const readOnly = degraded
  // Validation results and the draft hash are server facts that change without a revision bump: read them live.
  const lv = profile.last_validation
  const fresh = !dirty && !!lv && lv.draft_hash === profile.draft_hash && profile.revision === base.revision
  const canPublish = fresh && lv.valid && !degraded && !publishRunner.running
  const publishWhy = degraded ? t('profile.degradedWhy') : dirty ? t('profile.publish.dirty') : !lv ? t('profile.publish.notValidated') : !fresh ? t('profile.publish.stale') : !lv.valid ? t('profile.publish.invalid') : ''

  const saveDraft = () =>
    save.mutate(
      { orgId, mpfId: base.id, data: { doc, meta } },
      {
        onSuccess: (res) => {
          const saved = ok(res)
          setBase(saved)
          setDoc(saved.draft.doc)
          setMeta(saved.draft.meta)
          setConflict(null)
          toast.success(t('profile.saved'))
        },
        onError: (err) => {
          const cur = conflictCurrent<ModelProfileDto>(err)
          if (cur) setConflict(cur)
        },
      },
    )
  const rows = conflict
    ? Object.keys({ ...doc, ...conflict.draft.doc })
        .filter((k) => !sameJson((doc as Record<string, unknown>)[k], (conflict.draft.doc as Record<string, unknown>)[k]))
        .map((k) => ({ label: k, mine: JSON.stringify((doc as Record<string, unknown>)[k] ?? null), latest: JSON.stringify((conflict.draft.doc as Record<string, unknown>)[k] ?? null) }))
    : []

  const startValidate = () => validate.mutate({ orgId, mpfId: base.id }, { onSuccess: validateRunner.started, onError: (e) => toast.error(problemMessage(t, e)) })
  const startPublish = () =>
    publish.mutate(
      { orgId, mpfId: base.id },
      {
        onSuccess: publishRunner.started,
        onError: (e) => {
          const cur = conflictCurrent<ModelProfileDto>(e)
          if (cur) setConflict(cur)
          else toast.error(problemMessage(t, e))
        },
      },
    )
  const startRollback = (v: ModelProfileVersionDto) =>
    rollbackM.mutate(
      { orgId, mpfId: base.id, data: { version_id: v.id } },
      {
        onSuccess: (res) => {
          rollbackRunner.started(res)
          setRollback(null)
        },
        onError: (e) => toast.error(problemMessage(t, e)),
      },
    )

  const orqenIssues: CatherdIssue[] = issues.data?.issues ?? []
  const lastIssues: CatherdIssue[] = lv ? [...lv.errors, ...lv.warnings, ...lv.orqen_issues] : []
  const independence = issues.data?.independence ?? lv?.independence
  const sectionProps = (schemaData: NonNullable<typeof schema.data>): SectionProps => ({
    orgId,
    doc,
    meta,
    schema: schemaData,
    readOnly,
    onDoc: setDoc,
    onMeta: setMeta,
    enforcement: (role) => {
      const resolved = latest.data?.resolved as { roles?: Record<string, { enforcement?: string }> } | null | undefined
      return resolved?.roles?.[role]?.enforcement
    },
  })

  return (
    <>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          {embedded ? <h2 className="text-lg font-semibold">{base.name}</h2> : <h1 className="text-2xl font-semibold">{base.name}</h1>}
          <p className="text-sm text-muted-foreground">
            <Ltr>{base.slug}</Ltr>
            {base.description ? ` · ${base.description}` : ''}
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {independence && <IndependenceBadge independence={independence} />}
          {dirty && <Badge variant="outline">{t('policy.unsaved')}</Badge>}
        </div>
      </div>

      {degraded && (
        <Alert>
          <AlertTitle>{t('profile.degraded')}</AlertTitle>
          <AlertDescription>{t('profile.degradedWhy')}</AlertDescription>
        </Alert>
      )}
      {conflict && (
        <ConflictAlert
          rows={rows}
          onLoadLatest={() => {
            setBase(conflict)
            setDoc(conflict.draft.doc)
            setMeta(conflict.draft.meta)
            setConflict(null)
            save.reset()
          }}
          onKeepMine={() => {
            setBase(conflict)
            setConflict(null)
            save.reset()
          }}
        />
      )}

      <div className="grid gap-6 lg:grid-cols-[1fr_20rem]">
        <div className="min-w-0">
          {schema.isPending && <LoadingBlock />}
          {schema.isError && <LoadFailed title={t('profile.schemaFailed')} error={schema.error} onRetry={() => void schema.refetch()} />}
          {schema.data && (
            <Tabs defaultValue="roles">
              <div className="overflow-x-auto">
                <TabsList aria-label={t('profile.sections')}>
                  {EDITOR_TABS.map((k) => (
                    <TabsTrigger key={k} value={k}>
                      {t(`profile.tabs.${k}`)}
                    </TabsTrigger>
                  ))}
                </TabsList>
              </div>
              <TabsContent value="roles" className="pt-3">
                <RolesSection {...sectionProps(schema.data)} />
              </TabsContent>
              <TabsContent value="routing" className="pt-3">
                <RoutingSection {...sectionProps(schema.data)} />
              </TabsContent>
              <TabsContent value="access" className="pt-3">
                <AccessSection {...sectionProps(schema.data)} />
              </TabsContent>
              <TabsContent value="billing" className="pt-3">
                <BillingSection {...sectionProps(schema.data)} />
              </TabsContent>
              <TabsContent value="lifecycle" className="pt-3">
                <LifecycleSection {...sectionProps(schema.data)} />
              </TabsContent>
              <TabsContent value="advanced" className="pt-3">
                <AdvancedSection doc={doc} readOnly={readOnly} issues={orqenIssues} onDoc={setDoc} onJsonError={setJsonError} />
              </TabsContent>
            </Tabs>
          )}
        </div>

        <aside aria-label={t('profile.issues.title')} className="flex flex-col gap-4">
          <Card>
            <CardHeader>
              <CardTitle>{t('profile.issues.title')}</CardTitle>
            </CardHeader>
            <CardContent className="flex flex-col gap-3 text-sm">
              {issues.isPending && <LoadingBlock rows={2} />}
              {issues.isError && <p className="text-destructive">{problemMessage(t, issues.error)}</p>}
              {issues.isSuccess && orqenIssues.length === 0 && <p className="text-muted-foreground">{t('profile.issues.none')}</p>}
              <IssueList issues={orqenIssues} />
              {dirty && <p className="text-xs text-muted-foreground">{t('profile.issues.saveToRefresh')}</p>}
            </CardContent>
          </Card>
          <Card>
            <CardHeader>
              <CardTitle>{t('profile.validation.title')}</CardTitle>
            </CardHeader>
            <CardContent className="flex flex-col gap-3 text-sm">
              {!lv && <p className="text-muted-foreground">{t('profile.validation.never')}</p>}
              {lv && (
                <>
                  <div className="flex flex-wrap items-center gap-2">
                    <Badge variant={lv.valid ? 'default' : 'destructive'}>{lv.valid ? t('profile.validation.valid') : t('profile.validation.invalid')}</Badge>
                    {!fresh && <Badge variant="outline">{t('profile.validation.stale')}</Badge>}
                  </div>
                  <span className="text-xs text-muted-foreground">
                    {t('profile.validation.at', { time: formatDateTime(lv.checked_at, locale) })} · catherd <Ltr>{lv.catherd_version}</Ltr>
                  </span>
                  {!fresh && <p className="text-xs text-muted-foreground">{t('profile.validation.staleHelp')}</p>}
                  <IssueList issues={lastIssues} />
                </>
              )}
              {validateRunner.failed && <p className="text-destructive">{t('profile.validation.failedRun')}</p>}
            </CardContent>
          </Card>
        </aside>
      </div>

      <div className="flex flex-wrap items-center gap-2 rounded-lg border p-3" role="group" aria-label={t('profile.actions')}>
        <Button onClick={saveDraft} disabled={!dirty || save.isPending || !!jsonError || readOnly}>
          {t('profile.saveDraft')}
        </Button>
        <Button variant="outline" onClick={startValidate} disabled={dirty || degraded || validate.isPending || validateRunner.running}>
          {validateRunner.running ? t('profile.validation.running') : t('profile.validate')}
        </Button>
        <Button variant="outline" onClick={() => setDiffOpen('latest')}>
          <GitCompareIcon />
          {t('profile.previewDiff')}
        </Button>
        <Tooltip>
          <TooltipTrigger asChild>
            <span tabIndex={canPublish ? undefined : 0}>
              <Button onClick={startPublish} disabled={!canPublish || publish.isPending} aria-describedby={canPublish ? undefined : 'publish-why'}>
                {publishRunner.running ? t('profile.publish.running') : t('profile.publish.action')}
              </Button>
            </span>
          </TooltipTrigger>
          {!canPublish && <TooltipContent>{publishWhy}</TooltipContent>}
        </Tooltip>
        {!canPublish && (
          <span id="publish-why" className="text-xs text-muted-foreground">
            {publishWhy}
          </span>
        )}
        {jsonError && <span className="text-xs text-destructive">{t('profile.advanced.blocksSave')}</span>}
        {embedded && onDone && (
          <Button variant="ghost" className="ms-auto" onClick={onDone}>
            {t('profile.done')}
          </Button>
        )}
      </div>
      {publishRunner.failed && (
        <Alert variant="destructive" role="alert">
          <AlertTitle>{t('profile.publish.failed')}</AlertTitle>
          <AlertDescription>{t('profile.publish.failedHelp')}</AlertDescription>
        </Alert>
      )}

      <Card>
        <CardHeader>
          <CardTitle>{t('profile.versions.title')}</CardTitle>
        </CardHeader>
        <CardContent>
          {versions.isPending && <LoadingBlock rows={2} />}
          {versions.isError && <LoadFailed title={t('profile.versions.loadFailed')} error={versions.error} onRetry={() => void versions.refetch()} />}
          {versions.isSuccess && versions.data.length === 0 && <p className="text-sm text-muted-foreground">{t('profile.versions.empty')}</p>}
          {versions.isSuccess && versions.data.length > 0 && (
            <div className="overflow-x-auto rounded-lg border">
              <Table aria-label={t('profile.versions.title')}>
                <TableHeader>
                  <TableRow>
                    <TableHead>{t('profile.versions.number')}</TableHead>
                    <TableHead>{t('profile.versions.name')}</TableHead>
                    <TableHead>{t('profile.versions.state')}</TableHead>
                    <TableHead>{t('profile.versions.published')}</TableHead>
                    <TableHead>{t('common.actions')}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {versions.data.map((v) => (
                    <TableRow key={v.id}>
                      <TableCell className="tabular-nums">v{v.number}</TableCell>
                      <TableCell>
                        <Ltr>{v.catherd_name}</Ltr>
                        <div className="mt-1">
                          <Badge variant={staleHost(v) ? 'destructive' : 'outline'} data-host={v.host}>
                            {t('profile.versions.builtFor', { host: v.host })}
                          </Badge>
                        </div>
                      </TableCell>
                      <TableCell>
                        <Badge variant={v.state === 'failed' ? 'destructive' : v.state === 'published' ? 'default' : 'outline'}>{t(`profile.versions.states.${v.state}`)}</Badge>
                        {v.state === 'failed' && v.failure && <div className="mt-1 text-xs text-destructive">{v.failure.message}</div>}
                      </TableCell>
                      <TableCell className="text-xs">{formatDateTime(v.published_at ?? v.created_at, locale)}</TableCell>
                      <TableCell>
                        <div className="flex gap-1">
                          <Button size="sm" variant="outline" onClick={() => setDiffOpen(v.id)}>
                            {t('profile.versions.diff')}
                            <span className="sr-only"> v{v.number}</span>
                          </Button>
                          <Button size="sm" variant="outline" disabled={v.state !== 'published' || degraded || rollbackRunner.running} onClick={() => setRollback(v)}>
                            <RotateCcwIcon />
                            {staleHost(v) ? t('profile.versions.republish') : t('profile.versions.rollback')}
                            <span className="sr-only"> v{v.number}</span>
                          </Button>
                        </div>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          )}
        </CardContent>
      </Card>

      {diffOpen && <DiffSheet orgId={orgId} profileId={base.id} from={diffOpen} versions={versions.data ?? []} dirty={dirty} onClose={() => setDiffOpen(null)} />}

      {rollback && (
        <Dialog open onOpenChange={(o) => !o && setRollback(null)}>
          <DialogContent>
            <DialogHeader>
              <DialogTitle>{staleHost(rollback) ? t('profile.versions.republishTitle', { number: rollback.number, host: status.data?.host }) : t('profile.versions.rollbackTitle', { number: rollback.number })}</DialogTitle>
              <DialogDescription>{staleHost(rollback) ? t('profile.versions.republishBody') : t('profile.versions.rollbackBody')}</DialogDescription>
            </DialogHeader>
            {rollbackM.isError && <Alert variant="destructive"><AlertDescription>{problemMessage(t, rollbackM.error)}</AlertDescription></Alert>}
            <DialogFooter>
              <Button variant="outline" onClick={() => setRollback(null)}>
                {t('common.cancel')}
              </Button>
              <Button onClick={() => startRollback(rollback)} disabled={rollbackM.isPending}>
                {staleHost(rollback) ? t('profile.versions.republishConfirm') : t('profile.versions.rollbackConfirm')}
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      )}
    </>
  )
}

function IssueList({ issues }: { issues: CatherdIssue[] }) {
  const { t } = useTranslation()
  if (issues.length === 0) return null
  return (
    <ul className="flex flex-col gap-2">
      {issues.map((i, n) => (
        <li key={`${i.code}:${i.path_display}:${n}`} className="flex flex-col gap-0.5 rounded-md border p-2">
          <span className="flex flex-wrap items-center gap-2">
            <Badge variant={i.severity === 'error' ? 'destructive' : 'outline'}>{t(`profile.issues.severity.${i.severity}`)}</Badge>
            <span>{t(`profile.issues.codes.${i.code}`, { defaultValue: i.message })}</span>
          </span>
          {i.path_display && <Ltr className="text-xs text-muted-foreground">{i.path_display}</Ltr>}
          {i.fix && <span className="text-xs text-muted-foreground">{i.fix}</span>}
        </li>
      ))}
    </ul>
  )
}

function DiffSheet({ orgId, profileId, from, versions, dirty, onClose }: { orgId: string; profileId: string; from: string; versions: ModelProfileVersionDto[]; dirty: boolean; onClose: () => void }) {
  const { t } = useTranslation()
  const [chosen, setChosen] = useState(from)
  const diff = useProfileDiffQuery(orgId, profileId, chosen, 'draft')
  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent side="end" className="w-full overflow-y-auto sm:max-w-2xl">
        <SheetHeader>
          <SheetTitle>{t('profile.diff.title')}</SheetTitle>
          <SheetDescription>{t('profile.diff.help')}</SheetDescription>
        </SheetHeader>
        <div className="flex flex-col gap-4 p-4">
          <Select value={chosen} onValueChange={setChosen}>
            <SelectTrigger aria-label={t('profile.diff.compareWith')} className="w-64">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="latest">{t('profile.diff.latest')}</SelectItem>
              {versions.map((v) => (
                <SelectItem key={v.id} value={v.id}>
                  v{v.number} · {v.catherd_name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          {dirty && <p className="text-xs text-muted-foreground">{t('profile.diff.unsaved')}</p>}
          {diff.isPending && <LoadingBlock rows={2} />}
          {diff.isError && <LoadFailed title={t('profile.diff.failed')} error={diff.error} onRetry={() => void diff.refetch()} />}
          {diff.isSuccess && diff.data.changes.length === 0 && <p className="text-sm text-muted-foreground">{t('profile.diff.none')}</p>}
          {diff.isSuccess && diff.data.changes.length > 0 && (
            <Table aria-label={t('profile.diff.title')}>
              <TableHeader>
                <TableRow>
                  <TableHead>{t('profile.diff.path')}</TableHead>
                  <TableHead>{t('profile.diff.kind')}</TableHead>
                  <TableHead>{t('profile.diff.before')}</TableHead>
                  <TableHead>{t('profile.diff.after')}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {diff.data.changes.map((c) => (
                  <TableRow key={c.path_display}>
                    <TableCell className="whitespace-normal break-all">
                      <Ltr className="text-xs">{c.path_display}</Ltr>
                    </TableCell>
                    <TableCell>
                      <Badge variant="outline">{t(`profile.diff.kinds.${c.kind}`)}</Badge>
                    </TableCell>
                    <TableCell className="whitespace-normal break-all">
                      <Ltr className="text-xs">{c.before === undefined ? '—' : JSON.stringify(c.before)}</Ltr>
                    </TableCell>
                    <TableCell className="whitespace-normal break-all">
                      <Ltr className="text-xs">{c.after === undefined ? '—' : JSON.stringify(c.after)}</Ltr>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </div>
      </SheetContent>
    </Sheet>
  )
}
