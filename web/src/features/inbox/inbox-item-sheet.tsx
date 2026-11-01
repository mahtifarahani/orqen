import { useTranslation } from 'react-i18next'
import { Link } from 'react-router'
import { useState } from 'react'
import type { InboxItemDto } from '@/api/generated/model'
import { useInboxItem } from '@/api/hooks/inbox'
import { Ltr } from '@/components/ltr'
import { LoadFailed, LoadingBlock } from '@/components/query-state'
import { Badge } from '@/components/ui/badge'
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import { AnswerForm } from './answer-form'
import { EvidenceList } from './evidence-list'

/** The question text: translated by `question_code` with its params; the reason is the fallback. */
export function questionText(t: ReturnType<typeof useTranslation>['t'], item: InboxItemDto): string {
  const params = Object.fromEntries(Object.entries(item.params ?? {}).map(([k, v]) => [k, Array.isArray(v) ? v.join(', ') : String(v)]))
  return t(`inbox.question.${item.question_code}`, { defaultValue: t(`inbox.reason.${item.reason}`, { defaultValue: item.reason }), ...params })
}

export function InboxItemSheet({ orgId, id, onClose }: { orgId: string; id: string | null; onClose: () => void }) {
  const { t } = useTranslation()
  const item = useInboxItem(orgId, id)
  const [override, setOverride] = useState<InboxItemDto | null>(null)
  const current = override && override.id === id ? override : item.data
  return (
    <Sheet open={!!id} onOpenChange={(open) => { if (!open) { setOverride(null); onClose() } }}>
      <SheetContent side="end" className="overflow-y-auto sm:max-w-lg" aria-describedby="inbox-sheet-desc">
        <SheetHeader>
          <SheetTitle>{t('inbox.sheetTitle')}</SheetTitle>
          <SheetDescription id="inbox-sheet-desc">{t('inbox.sheetHelp')}</SheetDescription>
        </SheetHeader>
        <div className="flex flex-col gap-4 px-4 pb-6">
          {id && item.isPending && <LoadingBlock rows={3} />}
          {id && item.isError && <LoadFailed title={t('inbox.loadFailed')} error={item.error} onRetry={() => void item.refetch()} />}
          {current && (
            <>
              <div className="flex flex-wrap items-center gap-2">
                <Badge variant="outline">{t(`inbox.reason.${current.reason}`, { defaultValue: current.reason })}</Badge>
                <Badge variant={current.priority === 'high' ? 'destructive' : 'secondary'}>{t(`inbox.priority.${current.priority}`)}</Badge>
                <Badge variant="outline">{t(`inbox.status.${current.status}`)}</Badge>
                <Link to={`/orgs/${orgId}/tasks/${current.task_id}`} className="underline underline-offset-4">
                  <Ltr>{current.issue_key}</Ltr>
                </Link>
              </div>
              <p className="text-base" dir="auto">
                {questionText(t, current)}
              </p>
              {current.kind === 'approval' && <p className="text-sm text-muted-foreground">{t('inbox.approvalHint')}</p>}
              <EvidenceList evidence={current.evidence} />
              {current.status === 'open' ? (
                <AnswerForm key={`${current.id}-${current.revision}`} orgId={orgId} item={current} onLatest={setOverride} />
              ) : (
                <div className="text-sm text-muted-foreground" role="status">
                  <p>{t(`inbox.closed.${current.status}`)}</p>
                  {current.answers.map((a) => (
                    <p key={a.n} dir="auto">
                      {t(`inbox.option.${a.choice}`, { defaultValue: a.choice })}
                      {a.text ? `: ${a.text}` : ''}
                    </p>
                  ))}
                </div>
              )}
            </>
          )}
        </div>
      </SheetContent>
    </Sheet>
  )
}
