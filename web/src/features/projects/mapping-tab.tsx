import { IconButton } from '@/components/icon-button'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { PlusIcon, ShieldCheckIcon, Trash2Icon } from 'lucide-react'
import { toast } from 'sonner'
import type { ConfluenceRefInput, MappingInput, ProjectDto, RepoMappingInput } from '@/api/generated/model'
import { catalogRows, useCatalogProjects } from '@/api/hooks/catalog'
import { useConnections, useGitlabBranches, useJiraBoardsLookup, useJiraProjectsLookup } from '@/api/hooks/connections'
import { usePutMapping, useStartMappingCheck } from '@/api/hooks/projects'
import { ApiProblem, ok } from '@/api/problem'
import { ConflictAlert } from '@/components/conflict-alert'
import { Field } from '@/components/field'
import { Ltr } from '@/components/ltr'
import { useOperationRunner } from '@/components/use-operation-runner'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogTrigger } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import { conflictCurrent, sameJson } from '@/lib/conflict'
import { formatDateTime } from '@/lib/format'
import { problemMessage } from '@/lib/problem-message'
import { useLocale } from '@/lib/use-locale'

type RepoRow = RepoMappingInput & { path: string }
interface JiraDraft {
  connection_id: string
  project_id: string
  project_key: string
  board: { id: string; name: string } | null
  jql: string
  acceptance_field: string
}
interface Draft {
  repos: RepoRow[]
  jira: JiraDraft | null
  confluence: ConfluenceRefInput[]
}
const NONE = '__none'

const toMappingDraft = (p: ProjectDto): Draft => ({
  repos: p.mapping.repos.map((r) => ({
    connection_id: r.connection_id,
    provider_project_id: r.provider_project_id,
    role: r.role,
    target_branch: r.target_branch,
    package_path: r.package_path ?? '',
    note: r.note ?? '',
    path: r.path_snapshot,
  })),
  jira: p.mapping.jira
    ? {
        connection_id: p.mapping.jira.connection_id,
        project_id: p.mapping.jira.project_id,
        project_key: p.mapping.jira.project_key_snapshot,
        board: p.mapping.jira.board ? { id: p.mapping.jira.board.id, name: p.mapping.jira.board.name_snapshot ?? '' } : null,
        jql: p.mapping.jira.jql ?? '',
        acceptance_field: p.mapping.jira.acceptance_field ?? '',
      }
    : null,
  confluence: p.mapping.confluence.map((c) => ({ connection_id: c.connection_id, space_key: c.space_key ?? '', page_id: c.page_id ?? '', title: c.title_snapshot ?? '' })),
})

/** The request body: display paths are dropped, empty optional strings become null. */
const toMappingInput = (d: Draft): MappingInput => ({
  repos: d.repos.map(({ path: _path, ...r }) => {
    void _path
    return { ...r, package_path: r.package_path?.trim() || null, note: r.note?.trim() || null, target_branch: r.target_branch.trim() }
  }),
  jira: d.jira
    ? {
        connection_id: d.jira.connection_id,
        project_id: d.jira.project_id,
        project_key: d.jira.project_key || null,
        board: d.jira.board ? { id: d.jira.board.id, name: d.jira.board.name || null } : null,
        jql: d.jira.jql.trim() || null,
        acceptance_field: d.jira.acceptance_field.trim() || null,
      }
    : null,
  confluence: d.confluence.map((c) => ({ connection_id: c.connection_id, space_key: c.space_key?.trim() || null, page_id: c.page_id?.trim() || null, title: c.title?.trim() || null })),
})

export function MappingTab({ orgId, project }: { orgId: string; project: ProjectDto }) {
  const { t } = useTranslation()
  const locale = useLocale()
  const connections = useConnections(orgId)
  const [base, setBase] = useState(project)
  const [draft, setDraft] = useState<Draft>(toMappingDraft(project))
  const [conflict, setConflict] = useState<ProjectDto | null>(null)
  const dirty = !sameJson(draft, toMappingDraft(base))
  if (!dirty && project.revision > base.revision) {
    setBase(project)
    setDraft(toMappingDraft(project))
  }
  const put = usePutMapping(orgId, base.revision)
  const runner = useOperationRunner(orgId)
  const check = useStartMappingCheck(runner.key)

  const jiraConns = connections.data?.filter((c) => c.provider === 'jira') ?? []
  const confConns = connections.data?.filter((c) => c.provider === 'confluence') ?? []

  const fieldErrors = put.error instanceof ApiProblem && put.error.code !== 'revision_conflict' ? put.error.fieldErrors() : {}
  const general = put.isError && !conflict && Object.keys(fieldErrors).length === 0 ? problemMessage(t, put.error) : undefined

  const save = () =>
    put.mutate(
      { orgId, prjId: project.id, data: toMappingInput(draft) },
      {
        onSuccess: (res) => {
          const saved = ok(res)
          setBase(saved)
          setDraft(toMappingDraft(saved))
          setConflict(null)
          toast.success(t('mapping.saved'))
        },
        onError: (err) => {
          const cur = conflictCurrent<ProjectDto>(err)
          if (cur) setConflict(cur)
        },
      },
    )
  const setRepo = (i: number, patch: Partial<RepoRow>) => setDraft({ ...draft, repos: draft.repos.map((r, j) => (j === i ? { ...r, ...patch } : r)) })
  const makePrimary = (i: number) => setDraft({ ...draft, repos: draft.repos.map((r, j) => ({ ...r, role: j === i ? 'primary' : 'related' })) })

  const conflictRows = conflict
    ? [
        { label: t('mapping.repos'), mine: draft.repos.map((r) => r.path).join(', '), latest: conflict.mapping.repos.map((r) => r.path_snapshot).join(', ') },
        { label: t('mapping.jiraProject'), mine: draft.jira?.project_key ?? '', latest: conflict.mapping.jira?.project_key_snapshot ?? '' },
        { label: t('mapping.jql'), mine: draft.jira?.jql ?? '', latest: conflict.mapping.jira?.jql ?? '' },
      ].filter((r) => r.mine !== r.latest)
    : []

  // The check result is not part of the editable draft: always show the freshest one the server has.
  const lastCheck = project.mapping_check ?? base.mapping_check
  const stale = !!lastCheck && lastCheck.mapping_hash !== project.mapping_hash

  return (
    <div className="flex flex-col gap-4">
      {conflict && (
        <ConflictAlert
          rows={conflictRows}
          onLoadLatest={() => {
            setBase(conflict)
            setDraft(toMappingDraft(conflict))
            setConflict(null)
            put.reset()
          }}
          onKeepMine={() => {
            setBase(conflict)
            setConflict(null)
            put.reset()
          }}
        />
      )}

      <Card>
        <CardHeader className="flex-row items-center justify-between gap-2">
          <CardTitle>{t('mapping.repos')}</CardTitle>
          <AddRepoDialog
            orgId={orgId}
            existing={draft.repos}
            onAdd={(row) => setDraft({ ...draft, repos: [...draft.repos, { ...row, role: draft.repos.some((r) => r.role === 'primary') ? 'related' : 'primary' }] })}
          />
        </CardHeader>
        <CardContent className="flex flex-col gap-4">
          {draft.repos.length === 0 && <p className="text-sm text-muted-foreground">{t('mapping.noRepos')}</p>}
          {draft.repos.length > 0 && (
            <div role="radiogroup" aria-label={t('mapping.primaryGroup')} className="flex flex-col gap-4">
              {draft.repos.map((r, i) => (
                <div key={`${r.connection_id}:${r.provider_project_id}`} className="grid gap-3 rounded-lg border p-3 md:grid-cols-[1fr_1fr_1fr_auto]">
                  <div className="flex flex-col gap-2 md:col-span-4">
                    <div className="flex flex-wrap items-center gap-3">
                      <label className="flex items-center gap-2 text-sm">
                        <input type="radio" name="primary-repo" className="size-4 accent-primary" checked={r.role === 'primary'} onChange={() => makePrimary(i)} />
                        <Ltr>{r.path}</Ltr>
                      </label>
                      <Badge variant={r.role === 'primary' ? 'default' : 'outline'}>{t(`mapping.role.${r.role}`)}</Badge>
                    </div>
                  </div>
                  <Field id={`repo-${i}-branch`} label={t('mapping.targetBranch')} error={fieldErrors[`mapping.repos[${i}].target_branch`] ?? fieldErrors[`repos[${i}].target_branch`]}>
                    {(p) => <BranchInput {...p} orgId={orgId} repo={r} onChange={(v) => setRepo(i, { target_branch: v })} />}
                  </Field>
                  <Field id={`repo-${i}-pkg`} label={t('mapping.packagePath')} hint={t('mapping.packagePathHint')}>
                    {(p) => <Input {...p} dir="ltr" value={r.package_path ?? ''} onChange={(e) => setRepo(i, { package_path: e.target.value })} />}
                  </Field>
                  <Field id={`repo-${i}-note`} label={t('mapping.note')}>
                    {(p) => <Input {...p} value={r.note ?? ''} onChange={(e) => setRepo(i, { note: e.target.value })} />}
                  </Field>
                  <div className="flex items-end">
                    <IconButton type="button" variant="ghost" size="icon" aria-label={t('mapping.removeRepo', { path: r.path })} onClick={() => setDraft({ ...draft, repos: draft.repos.filter((_, j) => j !== i) })}>
                      <Trash2Icon />
                    </IconButton>
                  </div>
                </div>
              ))}
            </div>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>{t('mapping.jira')}</CardTitle>
        </CardHeader>
        <CardContent className="flex flex-col gap-4">
          <JiraSection orgId={orgId} jiraConns={jiraConns.map((c) => ({ id: c.id, name: c.name }))} jira={draft.jira} onChange={(jira) => setDraft({ ...draft, jira })} errors={fieldErrors} />
        </CardContent>
      </Card>

      <Card>
        <CardHeader className="flex-row items-center justify-between gap-2">
          <CardTitle>{t('mapping.confluence')}</CardTitle>
          <Button
            type="button"
            size="sm"
            variant="outline"
            disabled={confConns.length === 0}
            onClick={() => setDraft({ ...draft, confluence: [...draft.confluence, { connection_id: confConns[0]?.id ?? '', space_key: '', page_id: '', title: '' }] })}
          >
            <PlusIcon />
            {t('mapping.addConfluence')}
          </Button>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
          {confConns.length === 0 && <p className="text-sm text-muted-foreground">{t('mapping.noConfluence')}</p>}
          {draft.confluence.map((c, i) => (
            <div key={i} className="grid items-end gap-3 rounded-lg border p-3 md:grid-cols-[1fr_1fr_1fr_1fr_auto]">
              <Field id={`conf-${i}-con`} label={t('mapping.confluenceConnection')}>
                {(p) => (
                  <Select value={c.connection_id} onValueChange={(v) => setDraft({ ...draft, confluence: draft.confluence.map((x, j) => (j === i ? { ...x, connection_id: v } : x)) })}>
                    <SelectTrigger {...p} className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {confConns.map((cc) => (
                        <SelectItem key={cc.id} value={cc.id}>
                          {cc.name}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                )}
              </Field>
              {(['space_key', 'page_id', 'title'] as const).map((f) => (
                <Field key={f} id={`conf-${i}-${f}`} label={t(`mapping.confluenceFields.${f}`)}>
                  {(p) => <Input {...p} dir={f === 'title' ? undefined : 'ltr'} value={c[f] ?? ''} onChange={(e) => setDraft({ ...draft, confluence: draft.confluence.map((x, j) => (j === i ? { ...x, [f]: e.target.value } : x)) })} />}
                </Field>
              ))}
              <div className="flex items-center gap-2">
                <Badge variant="outline">{t('mapping.notVerified')}</Badge>
                <IconButton type="button" variant="ghost" size="icon" aria-label={t('common.remove')} onClick={() => setDraft({ ...draft, confluence: draft.confluence.filter((_, j) => j !== i) })}>
                  <Trash2Icon />
                </IconButton>
              </div>
            </div>
          ))}
        </CardContent>
      </Card>

      {general && (
        <Alert variant="destructive" role="alert">
          <AlertDescription>{general}</AlertDescription>
        </Alert>
      )}
      {Object.entries(fieldErrors).length > 0 && (
        <Alert variant="destructive" role="alert">
          <AlertDescription>
            <ul className="list-disc ps-5">
              {Object.entries(fieldErrors).map(([f, m]) => (
                <li key={f}>
                  <Ltr className="font-sans">{f}</Ltr>: {m}
                </li>
              ))}
            </ul>
          </AlertDescription>
        </Alert>
      )}
      <div className="flex flex-wrap items-center gap-3">
        <Button onClick={save} disabled={!dirty || put.isPending}>
          {t('mapping.save')}
        </Button>
        {dirty && (
          <Button variant="ghost" onClick={() => setDraft(toMappingDraft(base))}>
            {t('policy.discard')}
          </Button>
        )}
      </div>

      <Card>
        <CardHeader>
          <CardTitle>{t('mapping.check.title')}</CardTitle>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
          <div className="flex flex-wrap items-center gap-3">
            <Button
              variant="outline"
              disabled={dirty || check.isPending || runner.running}
              onClick={() => check.mutate({ orgId, prjId: project.id }, { onSuccess: runner.started, onError: (e) => toast.error(problemMessage(t, e)) })}
            >
              <ShieldCheckIcon />
              {runner.running ? t('mapping.check.running') : t('mapping.check.run')}
            </Button>
            {dirty && <span className="text-xs text-muted-foreground">{t('mapping.check.saveFirst')}</span>}
            {lastCheck && <span className="text-xs text-muted-foreground">{t('mapping.check.at', { time: formatDateTime(lastCheck.checked_at, locale) })}</span>}
            {stale && <Badge variant="outline">{t('mapping.check.stale')}</Badge>}
          </div>
          {runner.failed && <Alert variant="destructive"><AlertDescription>{t('mapping.check.failed')}</AlertDescription></Alert>}
          {lastCheck && (
            <ul className="flex flex-col gap-1 text-sm" aria-label={t('mapping.check.title')}>
              {lastCheck.results.map((r) => (
                <li key={r.check} className="flex flex-wrap items-baseline gap-2">
                  <Badge variant={r.status === 'failed' ? 'destructive' : 'outline'}>{t(`mapping.check.status.${r.status}`)}</Badge>
                  <span className="font-medium">{t(`mapping.check.names.${r.check}`, { defaultValue: r.check })}</span>
                  <span className="text-muted-foreground">{r.detail}</span>
                </li>
              ))}
            </ul>
          )}
        </CardContent>
      </Card>
    </div>
  )
}

function BranchInput({ orgId, repo, onChange, ...aria }: { orgId: string; repo: RepoRow; onChange: (v: string) => void; id: string }) {
  const { t } = useTranslation()
  const branches = useGitlabBranches(orgId, repo.connection_id, repo.provider_project_id, '')
  const listId = `${aria.id}-list`
  return (
    <>
      <Input
        {...aria}
        dir="ltr"
        list={listId}
        value={repo.target_branch}
        autoComplete="off"
        onChange={(e) => onChange(e.target.value)}
        placeholder={t('mapping.branchPlaceholder')}
      />
      <datalist id={listId}>
        {branches.data?.map((b) => (
          <option key={b.name} value={b.name}>
            {b.default ? `${b.name} (${t('mapping.defaultBranch')})` : b.name}
          </option>
        ))}
      </datalist>
      {branches.isError && <p className="text-xs text-muted-foreground">{t('mapping.branchesFailed')}</p>}
    </>
  )
}

function AddRepoDialog({ orgId, existing, onAdd }: { orgId: string; existing: RepoRow[]; onAdd: (r: RepoRow) => void }) {
  const { t } = useTranslation()
  const connections = useConnections(orgId)
  const gitlab = connections.data?.filter((c) => c.provider === 'gitlab') ?? []
  const [open, setOpen] = useState(false)
  const [conId, setConId] = useState<string>()
  const [text, setText] = useState('')
  const [search, setSearch] = useState('')
  useEffect(() => {
    const id = setTimeout(() => setSearch(text.trim()), 250)
    return () => clearTimeout(id)
  }, [text])
  const con = conId && gitlab.some((c) => c.id === conId) ? conId : gitlab[0]?.id
  const catalog = useCatalogProjects(orgId, open ? con : undefined, search ? { search } : {})
  const rows = catalogRows(catalog.data)
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button type="button" size="sm" variant="outline" disabled={gitlab.length === 0}>
          <PlusIcon />
          {t('mapping.addRepo')}
        </Button>
      </DialogTrigger>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>{t('mapping.addRepo')}</DialogTitle>
          <DialogDescription>{t('mapping.addRepoHelp')}</DialogDescription>
        </DialogHeader>
        <div className="flex flex-wrap gap-3">
          {gitlab.length > 1 && (
            <Select value={con} onValueChange={setConId}>
              <SelectTrigger aria-label={t('discover.connection')} className="w-48">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {gitlab.map((c) => (
                  <SelectItem key={c.id} value={c.id}>
                    {c.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )}
          <Input aria-label={t('discover.searchLabel')} type="search" className="max-w-xs" value={text} onChange={(e) => setText(e.target.value)} placeholder={t('discover.searchPlaceholder')} />
        </div>
        <ul className="flex max-h-72 flex-col divide-y overflow-y-auto rounded-lg border">
          {catalog.isPending && <li className="p-3 text-sm text-muted-foreground">{t('app.loading')}</li>}
          {catalog.isError && <li className="p-3 text-sm text-destructive">{problemMessage(t, catalog.error)}</li>}
          {catalog.isSuccess && rows.length === 0 && <li className="p-3 text-sm text-muted-foreground">{t('discover.noResults')}</li>}
          {rows.map((r) => {
            const taken = existing.some((e) => e.connection_id === r.connection_id && e.provider_project_id === r.provider_project_id)
            return (
              <li key={r.id} className="flex items-center justify-between gap-3 p-2 text-sm">
                <Ltr className="break-all">{r.path_with_namespace}</Ltr>
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  disabled={taken}
                  onClick={() => {
                    onAdd({ connection_id: r.connection_id, provider_project_id: r.provider_project_id, role: 'related', target_branch: r.default_branch ?? 'main', package_path: '', note: '', path: r.path_with_namespace })
                    setOpen(false)
                  }}
                >
                  {taken ? t('mapping.alreadyAdded') : t('mapping.add')}
                </Button>
              </li>
            )
          })}
        </ul>
        {catalog.hasNextPage && (
          <Button type="button" variant="outline" onClick={() => void catalog.fetchNextPage()}>
            {t('projects.loadMore')}
          </Button>
        )}
      </DialogContent>
    </Dialog>
  )
}

function JiraSection({
  orgId,
  jiraConns,
  jira,
  onChange,
  errors,
}: {
  orgId: string
  jiraConns: { id: string; name: string }[]
  jira: JiraDraft | null
  onChange: (j: JiraDraft | null) => void
  errors: Record<string, string>
}) {
  const { t } = useTranslation()
  const [text, setText] = useState('')
  const [query, setQuery] = useState('')
  useEffect(() => {
    const id = setTimeout(() => setQuery(text.trim()), 250)
    return () => clearTimeout(id)
  }, [text])
  const conId = jira?.connection_id ?? ''
  const projects = useJiraProjectsLookup(orgId, conId, query)
  const boards = useJiraBoardsLookup(orgId, conId, jira?.project_id ?? '')
  if (jiraConns.length === 0) return <p className="text-sm text-muted-foreground">{t('mapping.noJira')}</p>
  const blank = (connection_id: string): JiraDraft => ({ connection_id, project_id: '', project_key: '', board: null, jql: '', acceptance_field: '' })
  const projectOptions = [...(projects.data ?? [])]
  if (jira?.project_id && !projectOptions.some((p) => p.id === jira.project_id)) projectOptions.unshift({ id: jira.project_id, key: jira.project_key, name: '' })
  const boardOptions = [...(boards.data ?? [])]
  if (jira?.board && !boardOptions.some((b) => b.id === jira.board?.id)) boardOptions.unshift({ id: jira.board.id, name: jira.board.name, type: '' })

  return (
    <div className="grid gap-4 md:grid-cols-2">
      <Field id="jira-con" label={t('mapping.jiraConnection')}>
        {(p) => (
          <Select value={conId || NONE} onValueChange={(v) => onChange(v === NONE ? null : blank(v))}>
            <SelectTrigger {...p} className="w-full">
              <SelectValue placeholder={t('mapping.chooseJira')} />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={NONE}>{t('mapping.noJiraMapping')}</SelectItem>
              {jiraConns.map((c) => (
                <SelectItem key={c.id} value={c.id}>
                  {c.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
      </Field>
      {jira && (
        <>
          <div className="flex flex-col gap-2">
            <Label htmlFor="jira-search">{t('mapping.jiraProject')}</Label>
            <Input id="jira-search" type="search" value={text} onChange={(e) => setText(e.target.value)} placeholder={t('mapping.jiraSearch')} />
            <Select
              value={jira.project_id || NONE}
              onValueChange={(v) => {
                const p = projectOptions.find((x) => x.id === v)
                onChange({ ...jira, project_id: v === NONE ? '' : v, project_key: p?.key ?? '', board: null })
              }}
            >
              <SelectTrigger aria-label={t('mapping.jiraProject')} className="w-full">
                <SelectValue placeholder={t('mapping.chooseProject')} />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={NONE}>{t('mapping.chooseProject')}</SelectItem>
                {projectOptions.map((p) => (
                  <SelectItem key={p.id} value={p.id}>
                    {p.key}
                    {p.name ? ` — ${p.name}` : ''}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            {projects.isError && <p className="text-xs text-destructive">{problemMessage(t, projects.error)}</p>}
            {errors['mapping.jira.project_id'] && <p className="text-xs text-destructive">{errors['mapping.jira.project_id']}</p>}
          </div>
          <Field id="jira-board" label={t('mapping.jiraBoard')} hint={boards.isError ? t('mapping.boardsUnavailable') : undefined}>
            {(p) => (
              <Select
                value={jira.board?.id ?? NONE}
                onValueChange={(v) => {
                  const b = boardOptions.find((x) => x.id === v)
                  onChange({ ...jira, board: v === NONE || !b ? null : { id: b.id, name: b.name } })
                }}
                disabled={!jira.project_id}
              >
                <SelectTrigger {...p} className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={NONE}>{t('mapping.noBoard')}</SelectItem>
                  {boardOptions.map((b) => (
                    <SelectItem key={b.id} value={b.id}>
                      {b.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            )}
          </Field>
          <div className="md:col-span-2">
            <Field id="jira-jql" label={t('mapping.jql')} hint={t('mapping.jqlHint')} error={errors['mapping.jira.jql']}>
              {(p) => <Textarea {...p} dir="ltr" className="font-mono" rows={3} value={jira.jql} onChange={(e) => onChange({ ...jira, jql: e.target.value })} />}
            </Field>
          </div>
          <div className="md:col-span-2">
            <Field id="jira-acceptance-field" label={t('mapping.acceptanceField')} hint={t('mapping.acceptanceFieldHint')} error={errors['mapping.jira.acceptance_field']}>
              {(p) => <Input {...p} dir="ltr" className="font-mono" placeholder="customfield_10100" value={jira.acceptance_field} onChange={(e) => onChange({ ...jira, acceptance_field: e.target.value })} />}
            </Field>
          </div>
        </>
      )}
    </div>
  )
}
