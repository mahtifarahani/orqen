import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import type { CodeIndexDto } from '@/api/generated/model'
import { useCodeGraphStatus, useCodeImpact, useCodeIndexes, useRemoveIndex, useRetentionPreview, useStartIndex } from '@/api/hooks/codegraph'
import { projectRows, useProjects } from '@/api/hooks/projects'
import { Field } from '@/components/field'
import { Ltr } from '@/components/ltr'
import { LoadFailed, LoadingBlock } from '@/components/query-state'
import { useOperationRunner } from '@/components/use-operation-runner'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { NativeSelect } from '@/components/ui/native-select'
import { formatNumber } from '@/lib/format'
import { problemMessage } from '@/lib/problem-message'
import { useLocale } from '@/lib/use-locale'

/**
 * The code graph of the repositories: whether it is available, the indexes Orqen holds, indexing a project and a
 * read-only impact query against one ready index. Nothing here writes to a repository.
 */
export function CodeGraphPanel({ orgId }: { orgId: string }) {
  const { t } = useTranslation()
  const status = useCodeGraphStatus(orgId)
  const indexes = useCodeIndexes(orgId)
  const [selected, setSelected] = useState<string | null>(null)
  const ready = (indexes.data ?? []).filter((i) => i.state === 'ready')
  return (
    <section className="flex flex-col gap-4" aria-label={t('codeGraph.title')}>
      <div>
        <h2 className="text-lg font-semibold">{t('codeGraph.title')}</h2>
        <p className="text-sm text-muted-foreground">{t('codeGraph.subtitle')}</p>
      </div>
      {status.isPending && <LoadingBlock rows={1} />}
      {status.isError && <LoadFailed title={t('codeGraph.title')} error={status.error} onRetry={() => status.refetch()} />}
      {status.data && (
        <div className="flex flex-wrap items-center gap-2" role="status">
          <Badge variant={status.data.available ? 'default' : 'outline'} data-code-graph={status.data.available ? 'available' : 'unavailable'}>
            {t(status.data.available ? 'codeGraph.available' : 'codeGraph.unavailable')}
          </Badge>
          {!status.data.available && status.data.reason && (
            <span className="text-sm text-muted-foreground">{t(`extensions.unusable.${status.data.reason}`, { defaultValue: t(`problems.${status.data.reason}`, { defaultValue: status.data.reason }) })}</span>
          )}
          <span className="text-sm text-muted-foreground">{t('codeGraph.readyCount', { count: status.data.ready_indexes })}</span>
        </div>
      )}
      <StartIndex orgId={orgId} enabled={!!status.data?.available} />
      {indexes.isPending && <LoadingBlock rows={2} />}
      {indexes.isError && <LoadFailed title={t('codeGraph.indexes')} error={indexes.error} onRetry={() => indexes.refetch()} />}
      {indexes.data && <IndexList orgId={orgId} items={indexes.data} />}
      <ImpactQuery orgId={orgId} ready={ready} selected={selected} onSelect={setSelected} />
    </section>
  )
}

function StartIndex({ orgId, enabled }: { orgId: string; enabled: boolean }) {
  const { t } = useTranslation()
  const projects = projectRows(useProjects(orgId, '').data)
  const [prj, setPrj] = useState('')
  const runner = useOperationRunner(orgId)
  const start = useStartIndex(orgId, runner.key)
  return (
    <div className="flex flex-wrap items-end gap-2">
      <label className="flex min-w-0 flex-col gap-1 text-sm">
        {t('codeGraph.project')}
        <NativeSelect value={prj} onChange={(e) => setPrj(e.target.value)}>
          <option value="">{t('codeGraph.pickProject')}</option>
          {projects.map((p) => (
            <option key={p.id} value={p.id}>{p.name}</option>
          ))}
        </NativeSelect>
      </label>
      <Button
        variant="outline"
        disabled={!enabled || !prj || start.isPending || runner.running}
        onClick={() => start.mutate({ orgId, prjId: prj, data: {} }, { onSuccess: runner.started, onError: (e) => toast.error(problemMessage(t, e)) })}
      >
        {t(runner.running ? 'codeGraph.indexing' : 'codeGraph.index')}
      </Button>
      {!enabled && <span className="text-sm text-muted-foreground">{t('codeGraph.indexNeedsGraph')}</span>}
    </div>
  )
}

function IndexList({ orgId, items }: { orgId: string; items: CodeIndexDto[] }) {
  const { t } = useTranslation()
  const locale = useLocale()
  const [previewing, setPreviewing] = useState(false)
  const preview = useRetentionPreview(orgId, previewing)
  if (items.length === 0) return <p className="text-sm text-muted-foreground">{t('codeGraph.noIndexes')}</p>
  return (
    <div className="flex flex-col gap-2">
      <ul className="flex flex-col gap-2" aria-label={t('codeGraph.indexes')}>
        {items.map((i) => (
          <IndexRow key={i.id} orgId={orgId} index={i} />
        ))}
      </ul>
      <div className="flex flex-wrap items-center gap-2">
        <Button size="sm" variant="ghost" onClick={() => setPreviewing(true)}>
          {t('codeGraph.retention.preview')}
        </Button>
        {previewing && preview.data && (
          <span className="text-sm text-muted-foreground" role="status">
            {t('codeGraph.retention.result', { count: preview.data.removable.length, bytes: formatNumber(preview.data.total_bytes, locale) })}
          </span>
        )}
      </div>
    </div>
  )
}

function IndexRow({ orgId, index: i }: { orgId: string; index: CodeIndexDto }) {
  const { t } = useTranslation()
  const locale = useLocale()
  const remove = useRemoveIndex(orgId, i.revision)
  return (
    <li className="flex flex-wrap items-center justify-between gap-2 rounded-md border p-2 text-sm">
      <div className="flex flex-wrap items-center gap-2">
        <Badge variant={i.state === 'ready' ? 'default' : i.state === 'failed' ? 'destructive' : 'secondary'} data-index-state={i.state}>
          {t(`codeGraph.state.${i.state}`)}
        </Badge>
        {i.trigger === 'auto' && <Badge variant="outline">{t('codeGraph.trigger.auto')}</Badge>}
        <Ltr>{i.repo_key}</Ltr>
        <Ltr>{i.commit_sha.slice(0, 12)}</Ltr>
        <span className="text-muted-foreground">{t('codeGraph.size', { files: formatNumber(i.files, locale), bytes: formatNumber(i.bytes, locale) })}</span>
        {i.error_code && <span className="text-destructive">{t(`problems.${i.error_code}`, { defaultValue: i.error_code })}</span>}
      </div>
      {i.state !== 'removed' && (
        <Button size="sm" variant="ghost" disabled={remove.isPending} onClick={() => remove.mutate({ orgId, cixId: i.id }, { onError: (e) => toast.error(problemMessage(t, e)) })}>
          {t('codeGraph.remove')}
        </Button>
      )}
    </li>
  )
}

function ImpactQuery({ orgId, ready, selected, onSelect }: { orgId: string; ready: CodeIndexDto[]; selected: string | null; onSelect: (id: string | null) => void }) {
  const { t } = useTranslation()
  const [target, setTarget] = useState('')
  const [kind, setKind] = useState<'file' | 'symbol'>('file')
  const [depth, setDepth] = useState(1)
  const [asked, setAsked] = useState<{ id: string; target: string; kind: string; depth: number } | null>(null)
  const impact = useCodeImpact(orgId, asked?.id ?? null, asked?.target ?? '', asked?.kind ?? 'file', asked?.depth ?? 1)
  if (ready.length === 0) return null
  const id = selected ?? ready[0].id
  return (
    <div className="flex flex-col gap-3" aria-label={t('codeGraph.impact.title')} role="group">
      <h3 className="font-medium">{t('codeGraph.impact.title')}</h3>
      <form
        className="flex flex-wrap items-end gap-2"
        onSubmit={(e) => {
          e.preventDefault()
          if (target.trim()) setAsked({ id, target: target.trim(), kind, depth })
        }}
      >
        <label className="flex flex-col gap-1 text-sm">
          {t('codeGraph.impact.index')}
          <NativeSelect value={id} onChange={(e) => onSelect(e.target.value)}>
            {ready.map((r) => (
              <option key={r.id} value={r.id}>{r.repo_key} {r.commit_sha.slice(0, 8)}</option>
            ))}
          </NativeSelect>
        </label>
        <label className="flex flex-col gap-1 text-sm">
          {t('codeGraph.impact.kind')}
          <NativeSelect value={kind} onChange={(e) => setKind(e.target.value as 'file' | 'symbol')}>
            <option value="file">{t('codeGraph.impact.file')}</option>
            <option value="symbol">{t('codeGraph.impact.symbol')}</option>
          </NativeSelect>
        </label>
        <Field id="impact-target" label={t('codeGraph.impact.target')}>
          {(f) => <Input {...f} dir="ltr" value={target} onChange={(e) => setTarget(e.target.value)} />}
        </Field>
        <label className="flex flex-col gap-1 text-sm">
          {t('codeGraph.impact.depth')}
          <NativeSelect value={depth} onChange={(e) => setDepth(Number(e.target.value))}>
            <option value={1}>1</option>
            <option value={2}>2</option>
          </NativeSelect>
        </label>
        <Button type="submit" variant="outline" disabled={!target.trim()}>{t('codeGraph.impact.ask')}</Button>
      </form>
      {impact.isFetching && <LoadingBlock rows={1} />}
      {impact.isError && (
        <Alert variant="destructive" role="alert">
          <AlertDescription>{problemMessage(t, impact.error)}</AlertDescription>
        </Alert>
      )}
      {impact.data && (
        <div className="flex flex-col gap-1 text-sm">
          {impact.data.items.length === 0 && <p className="text-muted-foreground">{t('codeGraph.impact.none')}</p>}
          <ul className="flex flex-col gap-1" aria-label={t('codeGraph.impact.results')}>
            {impact.data.items.map((it, n) => (
              <li key={`${it.path}-${it.symbol}-${n}`} className="flex flex-wrap items-center gap-2">
                <Badge variant="outline">{it.relation}</Badge>
                <Ltr className="break-all">{it.path}{it.symbol ? `#${it.symbol}` : ''}</Ltr>
                <span className="text-xs text-muted-foreground">{t('codeGraph.impact.depthN', { depth: it.depth })}</span>
              </li>
            ))}
          </ul>
          {impact.data.truncated && <Badge variant="outline" className="self-start">{t('codeGraph.impact.truncated')}</Badge>}
        </div>
      )}
    </div>
  )
}
