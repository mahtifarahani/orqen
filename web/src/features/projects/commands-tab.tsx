import { IconButton } from '@/components/icon-button'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { CheckIcon, PencilIcon, PlusIcon, Trash2Icon, XIcon } from 'lucide-react'
import { toast } from 'sonner'
import type { CommandInput, CommandSpec, CommandSuggestion, ProjectDto } from '@/api/generated/model'
import { usePutCommands, useContractSnapshot } from '@/api/hooks/projects'
import { ApiProblem, ok } from '@/api/problem'
import { ConflictAlert } from '@/components/conflict-alert'
import { Field } from '@/components/field'
import { Ltr } from '@/components/ltr'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { conflictCurrent } from '@/lib/conflict'
import { problemMessage } from '@/lib/problem-message'

const KINDS = ['setup', 'lint', 'typecheck', 'test', 'build', 'custom'] as const
type Kind = (typeof KINDS)[number]

/** A stored command as the request body. Approval is sent explicitly; argv stays an array. */
function specToInput(c: CommandSpec): CommandInput {
  return {
    key: c.key,
    kind: c.kind,
    argv: c.argv,
    cwd: c.cwd,
    env: c.env.map((e) => ({ name: e.name, source: { kind: e.source.kind, value: e.source.value ?? null, var: e.source.var ?? null } })),
    timeout_secs: c.timeout_secs,
    needs_network: c.needs_network,
    needs_docker: c.needs_docker,
    origin: { kind: c.origin.kind, snapshot_id: c.origin.snapshot_id ?? null, file: c.origin.file ?? null },
    approved: c.approved,
  }
}

export function CommandsTab({ orgId, project }: { orgId: string; project: ProjectDto }) {
  const { t } = useTranslation()
  const [base, setBase] = useState(project)
  if (project.revision > base.revision) setBase(project)
  const put = usePutCommands(orgId, base.revision)
  const [editing, setEditing] = useState<{ index: number | null; spec: CommandInput } | null>(null)
  const [conflict, setConflict] = useState<{ project: ProjectDto; pending: CommandInput[] } | null>(null)
  const snapshot = useContractSnapshot(orgId, project.id, project.latest_contract_snapshot_id)
  const commands = base.commands

  const write = (next: CommandInput[]) =>
    put.mutate(
      { orgId, prjId: project.id, data: { commands: next } },
      {
        onSuccess: (res) => {
          setBase(ok(res))
          setConflict(null)
          setEditing(null)
          toast.success(t('commands.saved'))
        },
        onError: (err) => {
          const cur = conflictCurrent<ProjectDto>(err)
          if (cur) setConflict({ project: cur, pending: next })
        },
      },
    )
  const inputs = commands.map(specToInput)
  const approve = (i: number, approved: boolean) => write(inputs.map((c, j) => (j === i ? { ...c, approved } : c)))
  const remove = (i: number) => write(inputs.filter((_, j) => j !== i))
  const upsert = (index: number | null, spec: CommandInput) => write(index === null ? [...inputs, spec] : inputs.map((c, j) => (j === index ? spec : c)))
  const hasKey = (key: string) => commands.some((c) => c.key === key)

  const accept = (s: CommandSuggestion) => {
    const snap = snapshot.data
    write([
      ...inputs,
      {
        key: hasKey(s.key) ? `${s.key}-2` : s.key,
        kind: s.kind,
        argv: s.argv,
        cwd: s.cwd,
        origin: { kind: 'contract', snapshot_id: snap?.id ?? null, file: s.source_file },
        approved: false,
      },
    ])
  }

  return (
    <div className="flex flex-col gap-4">
      {conflict && (
        <ConflictAlert
          rows={[{ label: t('commands.title'), mine: String(conflict.pending.length), latest: String(conflict.project.commands.length) }]}
          onLoadLatest={() => {
            setBase(conflict.project)
            setConflict(null)
            put.reset()
          }}
          onKeepMine={() => {
            setBase(conflict.project)
            write(conflict.pending)
            put.reset()
          }}
        />
      )}
      <Card>
        <CardHeader className="flex-row items-center justify-between gap-2">
          <CardTitle>{t('commands.title')}</CardTitle>
          <Button size="sm" onClick={() => setEditing({ index: null, spec: { key: '', kind: 'test', argv: [''], approved: false } })}>
            <PlusIcon />
            {t('commands.add')}
          </Button>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
          <p className="max-w-prose text-sm text-muted-foreground">{t('commands.help')}</p>
          {put.isError && !conflict && !editing && (
            <Alert variant="destructive" role="alert">
              <AlertDescription>{problemMessage(t, put.error)}</AlertDescription>
            </Alert>
          )}
          {commands.length === 0 ? (
            <p className="text-sm text-muted-foreground">{t('commands.empty')}</p>
          ) : (
            <div className="overflow-x-auto rounded-lg border">
              <Table aria-label={t('commands.title')}>
                <TableHeader>
                  <TableRow>
                    <TableHead>{t('commands.columns.key')}</TableHead>
                    <TableHead>{t('commands.columns.command')}</TableHead>
                    <TableHead>{t('commands.columns.cwd')}</TableHead>
                    <TableHead>{t('commands.columns.approval')}</TableHead>
                    <TableHead>{t('common.actions')}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {commands.map((c, i) => (
                    <TableRow key={c.key}>
                      <TableCell>
                        <div className="flex flex-col gap-1">
                          <Ltr>{c.key}</Ltr>
                          <Badge variant="outline">{t(`commands.kind.${c.kind}`)}</Badge>
                        </div>
                      </TableCell>
                      <TableCell className="whitespace-normal">
                        <Ltr className="break-all text-xs">{c.argv.join(' ')}</Ltr>
                        {c.origin.kind === 'contract' && <Badge variant="secondary" className="ms-2">{t('commands.fromContract')}</Badge>}
                      </TableCell>
                      <TableCell>
                        <Ltr className="text-xs">{c.cwd}</Ltr>
                      </TableCell>
                      <TableCell>
                        {c.approved ? (
                          <Badge>
                            <CheckIcon aria-hidden />
                            {t('commands.approved')}
                          </Badge>
                        ) : (
                          <Badge variant="outline">{t('commands.notApproved')}</Badge>
                        )}
                      </TableCell>
                      <TableCell>
                        <div className="flex flex-wrap gap-1">
                          <Button size="sm" variant="outline" disabled={put.isPending} onClick={() => approve(i, !c.approved)}>
                            {c.approved ? t('commands.revoke') : t('commands.approve')}
                            <span className="sr-only"> {c.key}</span>
                          </Button>
                          <IconButton size="icon-sm" variant="ghost" aria-label={t('commands.editCommand', { key: c.key })} onClick={() => setEditing({ index: i, spec: inputs[i] })}>
                            <PencilIcon />
                          </IconButton>
                          <IconButton size="icon-sm" variant="ghost" aria-label={t('commands.removeCommand', { key: c.key })} disabled={put.isPending} onClick={() => remove(i)}>
                            <Trash2Icon />
                          </IconButton>
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

      <Card>
        <CardHeader>
          <CardTitle>{t('commands.suggestions.title')}</CardTitle>
        </CardHeader>
        <CardContent className="flex flex-col gap-2">
          {!project.latest_contract_snapshot_id && <p className="text-sm text-muted-foreground">{t('commands.suggestions.noContract')}</p>}
          {snapshot.isPending && project.latest_contract_snapshot_id && <p className="text-sm text-muted-foreground">{t('app.loading')}</p>}
          {snapshot.isError && <p className="text-sm text-destructive">{problemMessage(t, snapshot.error)}</p>}
          {snapshot.data && snapshot.data.suggestions.length === 0 && <p className="text-sm text-muted-foreground">{t('commands.suggestions.none')}</p>}
          {snapshot.data && snapshot.data.suggestions.length > 0 && <p className="text-xs text-muted-foreground">{t('commands.suggestions.help')}</p>}
          <ul className="flex flex-col divide-y">
            {snapshot.data?.suggestions.map((s) => {
              const added = commands.some((c) => c.argv.join('\u0000') === s.argv.join('\u0000') && c.cwd === s.cwd)
              return (
                <li key={s.key} className="flex flex-wrap items-center justify-between gap-2 py-2 text-sm">
                  <div className="flex flex-col gap-1">
                    <span className="flex items-center gap-2">
                      <Badge variant="outline">{t(`commands.kind.${s.kind}`)}</Badge>
                      <Ltr className="text-xs">{s.argv.join(' ')}</Ltr>
                    </span>
                    <span className="text-xs text-muted-foreground">
                      <Ltr>{s.source_file}</Ltr> · <Ltr className="font-sans">{s.source_detail}</Ltr>
                    </span>
                  </div>
                  <Button size="sm" variant="outline" disabled={added || put.isPending} onClick={() => accept(s)}>
                    {added ? t('commands.suggestions.added') : t('commands.suggestions.accept')}
                    <span className="sr-only"> {s.key}</span>
                  </Button>
                </li>
              )
            })}
          </ul>
        </CardContent>
      </Card>

      {editing && (
        <CommandDialog
          key={`${editing.index}`}
          initial={editing.spec}
          existingKeys={commands.filter((_, i) => i !== editing.index).map((c) => c.key)}
          original={editing.index === null ? null : commands[editing.index]}
          pending={put.isPending}
          error={put.error}
          onCancel={() => {
            setEditing(null)
            put.reset()
          }}
          onSave={(spec) => upsert(editing.index, spec)}
        />
      )}
    </div>
  )
}

interface EnvRow {
  name: string
  kind: 'literal' | 'host'
  value: string
}

/** Argv is edited as separate chips (one input per argument); it is never joined into one string. */
function CommandDialog({
  initial,
  original,
  existingKeys,
  pending,
  error,
  onSave,
  onCancel,
}: {
  initial: CommandInput
  original: CommandSpec | null
  existingKeys: string[]
  pending: boolean
  error: unknown
  onSave: (c: CommandInput) => void
  onCancel: () => void
}) {
  const { t } = useTranslation()
  const [key, setKey] = useState(initial.key)
  const [kind, setKind] = useState<Kind>(initial.kind)
  const [argv, setArgv] = useState<string[]>(initial.argv.length ? initial.argv : [''])
  const [cwd, setCwd] = useState(initial.cwd ?? '')
  const [timeout, setTimeoutSecs] = useState(String(initial.timeout_secs ?? 600))
  const [network, setNetwork] = useState(!!initial.needs_network)
  const [docker, setDocker] = useState(!!initial.needs_docker)
  const [approved, setApproved] = useState(!!initial.approved)
  const [touched, setTouched] = useState(false)
  const [env, setEnv] = useState<EnvRow[]>(
    (initial.env ?? []).map((e) => ({ name: e.name, kind: e.source.kind === 'host' ? 'host' : 'literal', value: (e.source.kind === 'host' ? e.source.var : e.source.value) ?? '' })),
  )
  const fieldErrors = error instanceof ApiProblem ? error.fieldErrors() : {}
  const keyDup = existingKeys.includes(key)
  const cleaned = argv.map((a) => a).filter((a) => a !== '')
  const valid = /^[a-z][a-z0-9-]{0,31}$/.test(key) && !keyDup && cleaned.length > 0 && Number(timeout) >= 1 && Number(timeout) <= 7200
  const changed =
    !!original &&
    (JSON.stringify(cleaned) !== JSON.stringify(original.argv) || (cwd || '.') !== original.cwd || Number(timeout) !== original.timeout_secs || network !== original.needs_network || docker !== original.needs_docker || JSON.stringify(env.map((e) => [e.name, e.kind, e.value])) !== JSON.stringify(original.env.map((e) => [e.name, e.source.kind, (e.source.kind === 'host' ? e.source.var : e.source.value) ?? ''])))

  // Changing what runs revokes an earlier approval unless the user approves the new version explicitly.
  const shownApproved = changed && !touched ? false : approved

  const submit = () =>
    onSave({
      key,
      kind,
      argv: cleaned,
      cwd: cwd.trim() === '' ? null : cwd.trim(),
      env: env.filter((e) => e.name).map((e) => ({ name: e.name, source: e.kind === 'host' ? { kind: 'host' as const, var: e.value || e.name, value: null } : { kind: 'literal' as const, value: e.value, var: null } })),
      timeout_secs: Number(timeout),
      needs_network: network,
      needs_docker: docker,
      origin: initial.origin ?? null,
      approved: shownApproved,
    })

  const errorList = Object.entries(fieldErrors)
  return (
    <Dialog open onOpenChange={(o) => !o && onCancel()}>
      <DialogContent className="max-h-[90vh] max-w-2xl overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{original ? t('commands.edit') : t('commands.add')}</DialogTitle>
          <DialogDescription>{t('commands.dialogHelp')}</DialogDescription>
        </DialogHeader>
        <form
          className="flex flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault()
            if (valid && !pending) submit()
          }}
        >
          <div className="grid gap-4 sm:grid-cols-2">
            <Field id="cmd-key" label={t('commands.fields.key')} hint={t('commands.fields.keyHint')} error={keyDup ? t('commands.keyDuplicate') : (fieldErrors['commands[0].key'] ?? undefined)}>
              {(p) => <Input {...p} dir="ltr" value={key} onChange={(e) => setKey(e.target.value)} disabled={!!original} />}
            </Field>
            <Field id="cmd-kind" label={t('commands.fields.kind')}>
              {(p) => (
                <Select value={kind} onValueChange={(v) => setKind(v as Kind)}>
                  <SelectTrigger {...p} className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {KINDS.map((k) => (
                      <SelectItem key={k} value={k}>
                        {t(`commands.kind.${k}`)}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              )}
            </Field>
          </div>

          <fieldset className="flex flex-col gap-2">
            <legend className="mb-1 text-sm font-medium">{t('commands.fields.argv')}</legend>
            <p className="text-xs text-muted-foreground">{t('commands.fields.argvHint')}</p>
            <ol className="flex flex-wrap gap-2" aria-label={t('commands.fields.argv')}>
              {argv.map((a, i) => (
                <li key={i} className="flex items-center gap-1">
                  <Input
                    aria-label={t('commands.fields.argvItem', { n: i + 1 })}
                    dir="ltr"
                    className="h-8 w-40 font-mono text-xs"
                    value={a}
                    onChange={(e) => setArgv(argv.map((x, j) => (j === i ? e.target.value : x)))}
                  />
                  <IconButton type="button" size="icon-sm" variant="ghost" aria-label={t('commands.fields.removeArg', { n: i + 1 })} disabled={argv.length === 1} onClick={() => setArgv(argv.filter((_, j) => j !== i))}>
                    <XIcon />
                  </IconButton>
                </li>
              ))}
              <li>
                <Button type="button" size="sm" variant="outline" onClick={() => setArgv([...argv, ''])}>
                  <PlusIcon />
                  {t('commands.fields.addArg')}
                </Button>
              </li>
            </ol>
          </fieldset>

          <div className="grid gap-4 sm:grid-cols-2">
            <Field id="cmd-cwd" label={t('commands.fields.cwd')} hint={t('commands.fields.cwdHint')}>
              {(p) => <Input {...p} dir="ltr" value={cwd} onChange={(e) => setCwd(e.target.value)} />}
            </Field>
            <Field id="cmd-timeout" label={t('commands.fields.timeout')} hint={t('commands.fields.timeoutHint')}>
              {(p) => <Input {...p} type="number" dir="ltr" min={1} max={7200} value={timeout} onChange={(e) => setTimeoutSecs(e.target.value)} />}
            </Field>
          </div>

          <fieldset className="flex flex-col gap-2">
            <legend className="mb-1 text-sm font-medium">{t('commands.fields.env')}</legend>
            {env.map((e, i) => (
              <div key={i} className="flex flex-wrap items-center gap-2">
                <Input aria-label={t('commands.fields.envName')} dir="ltr" className="h-8 w-40 font-mono text-xs" value={e.name} onChange={(ev) => setEnv(env.map((x, j) => (j === i ? { ...x, name: ev.target.value } : x)))} />
                <Select value={e.kind} onValueChange={(v) => setEnv(env.map((x, j) => (j === i ? { ...x, kind: v as EnvRow['kind'] } : x)))}>
                  <SelectTrigger aria-label={t('commands.fields.envSource')} className="h-8 w-40">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="literal">{t('commands.fields.envLiteral')}</SelectItem>
                    <SelectItem value="host">{t('commands.fields.envHost')}</SelectItem>
                  </SelectContent>
                </Select>
                <Input aria-label={t('commands.fields.envValue')} dir="ltr" className="h-8 w-48 font-mono text-xs" value={e.value} onChange={(ev) => setEnv(env.map((x, j) => (j === i ? { ...x, value: ev.target.value } : x)))} />
                <IconButton type="button" size="icon-sm" variant="ghost" aria-label={t('common.remove')} onClick={() => setEnv(env.filter((_, j) => j !== i))}>
                  <Trash2Icon />
                </IconButton>
              </div>
            ))}
            <div>
              <Button type="button" size="sm" variant="outline" onClick={() => setEnv([...env, { name: '', kind: 'host', value: '' }])}>
                <PlusIcon />
                {t('commands.fields.addEnv')}
              </Button>
            </div>
          </fieldset>

          <div className="flex flex-wrap gap-6">
            <div className="flex items-center gap-2">
              <Switch id="cmd-net" checked={network} onCheckedChange={setNetwork} />
              <Label htmlFor="cmd-net">{t('commands.fields.network')}</Label>
            </div>
            <div className="flex items-center gap-2">
              <Switch id="cmd-docker" checked={docker} onCheckedChange={setDocker} />
              <Label htmlFor="cmd-docker">{t('commands.fields.docker')}</Label>
            </div>
            <div className="flex items-center gap-2">
              <Switch id="cmd-approved" checked={shownApproved} onCheckedChange={(v) => {
                setTouched(true)
                setApproved(v)
              }} />
              <Label htmlFor="cmd-approved">{t('commands.fields.approved')}</Label>
            </div>
          </div>
          {changed && original?.approved && <p className="text-xs text-muted-foreground">{t('commands.revokedOnChange')}</p>}

          {errorList.length > 0 && (
            <Alert variant="destructive" role="alert">
              <AlertDescription>
                <ul className="list-disc ps-5">
                  {errorList.map(([f, m]) => (
                    <li key={f}>
                      <Ltr className="font-sans">{f}</Ltr>: {m}
                    </li>
                  ))}
                </ul>
              </AlertDescription>
            </Alert>
          )}
          {!!error && errorList.length === 0 && (
            <Alert variant="destructive" role="alert">
              <AlertDescription>{problemMessage(t, error)}</AlertDescription>
            </Alert>
          )}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onCancel}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" disabled={!valid || pending}>
              {t('common.save')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
