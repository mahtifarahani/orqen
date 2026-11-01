import { useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { CopyIcon } from 'lucide-react'
import { Link } from 'react-router'
import { toast } from 'sonner'
import type { InboxItemDto } from '@/api/generated/model'
import { useAnswerInbox, useResolveInbox } from '@/api/hooks/inbox'
import { ApiProblem } from '@/api/problem'
import { ConflictAlert } from '@/components/conflict-alert'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { problemMessage } from '@/lib/problem-message'
import { useIntentKey } from '@/lib/use-intent-key'
import { cn } from '@/lib/utils'

const RESOLVE_ONLY = ['credential_expired', 'project_mapping', 'dependency_unavailable', 'unsupported_toolchain', 'policy_conflict']

/** Answer an open question: choose an option, write text when it needs one, then "Answer and resume". */
export function AnswerForm({ orgId, item, onLatest }: { orgId: string; item: InboxItemDto; onLatest: (current: InboxItemDto) => void }) {
  const { t } = useTranslation()
  const [choice, setChoice] = useState(item.options[0]?.id ?? '')
  const [text, setText] = useState('')
  const [key, renew] = useIntentKey()
  const [resolveKey, renewResolve] = useIntentKey()
  const answer = useAnswerInbox(orgId, { revision: item.revision, idempotencyKey: key })
  const resolve = useResolveInbox(orgId, { revision: item.revision, idempotencyKey: resolveKey })
  const [sent, setSent] = useState(false) // an answered question stays closed until the refreshed item arrives
  const [stale, setStale] = useState<InboxItemDto | null>(null)
  const [conflict, setConflict] = useState<InboxItemDto | null>(null)
  const inflight = useRef(false) // a double click must not send twice before the button re-renders as disabled
  const option = item.options.find((o) => o.id === choice)
  const needsText = !!option?.requires_text
  const textMissing = needsText && text.trim() === ''
  const fieldErrors = answer.error instanceof ApiProblem && answer.error.code === 'answer_invalid' ? answer.error.fieldErrors() : {}

  const onError = (e: unknown) => {
    if (e instanceof ApiProblem && e.code === 'inbox_item_stale') setStale((e.current as InboxItemDto) ?? item)
    else if (e instanceof ApiProblem && e.code === 'revision_conflict') setConflict((e.current as InboxItemDto) ?? null)
    else if (!(e instanceof ApiProblem && e.code === 'answer_invalid')) toast.error(problemMessage(t, e))
  }
  const submit = () => {
    if (inflight.current) return
    inflight.current = true
    answer.mutate(
      { orgId, inbId: item.id, data: { choice, ...(text.trim() ? { text: text.trim() } : {}) } },
      {
        onSuccess: () => {
          setSent(true)
          renew()
          toast.success(t('inbox.answered'))
        },
        onError,
        onSettled: () => {
          inflight.current = false
        },
      },
    )
  }

  if (stale) {
    return (
      <Alert variant="destructive" role="alert" data-testid="stale-alert">
        <AlertTitle>{t('inbox.stale')}</AlertTitle>
        <AlertDescription className="flex flex-col gap-3">
          <p>{t('inbox.staleBody')}</p>
          {stale.superseded_by_run && (
            <Link to={`/orgs/${orgId}/tasks/${stale.task_id}`} className="underline underline-offset-4">
              {t('inbox.staleLink')}
            </Link>
          )}
          {text && (
            <div className="flex flex-col gap-1">
              <Label htmlFor="kept-text">{t('inbox.yourText')}</Label>
              <Textarea id="kept-text" dir="auto" readOnly value={text} />
              <Button size="sm" variant="outline" className="self-start" onClick={() => void navigator.clipboard?.writeText(text)}>
                <CopyIcon />
                {t('inbox.copy')}
              </Button>
            </div>
          )}
          <Button size="sm" variant="outline" className="self-start" onClick={() => onLatest(stale)}>
            {t('inbox.showCurrent')}
          </Button>
        </AlertDescription>
      </Alert>
    )
  }

  return (
    <div className="flex flex-col gap-4">
      {conflict && (
        <ConflictAlert
          rows={[]}
          onLoadLatest={() => {
            onLatest(conflict)
            setConflict(null)
            answer.reset()
          }}
          onKeepMine={() => {
            onLatest(conflict) // rebase onto the latest revision, the typed text stays
            setConflict(null)
            answer.reset()
          }}
        />
      )}
      <fieldset className="flex flex-col gap-2">
        <legend className="mb-1 text-sm font-medium">{t('inbox.options')}</legend>
        {item.options.map((o) => (
          <div key={o.id} className={cn('flex items-start gap-2 rounded-md border p-2 text-sm', choice === o.id && 'border-primary bg-muted')}>
            <input id={`opt-${item.id}-${o.id}`} type="radio" name={`choice-${item.id}`} value={o.id} checked={choice === o.id} onChange={() => setChoice(o.id)} className="mt-1" />
            <Label htmlFor={`opt-${item.id}-${o.id}`} className="flex cursor-pointer flex-col items-start gap-0.5 font-normal">
              <span className="font-medium">{t(`inbox.option.${o.id}`, { defaultValue: o.id })}</span>
              <span className="text-xs text-muted-foreground">{t(`inbox.effect.${o.effect_code}`, { defaultValue: '' })}</span>
            </Label>
          </div>
        ))}
      </fieldset>
      {needsText && (
        <div className="flex flex-col gap-2">
          <Label htmlFor="answer-text">{t('inbox.textLabel')}</Label>
          <Textarea id="answer-text" dir="auto" rows={4} maxLength={4000} value={text} aria-invalid={!!fieldErrors.text} onChange={(e) => setText(e.target.value)} />
          {fieldErrors.text && (
            <p role="alert" className="text-xs text-destructive">
              {fieldErrors.text}
            </p>
          )}
        </div>
      )}
      {fieldErrors.choice && (
        <p role="alert" className="text-xs text-destructive">
          {fieldErrors.choice}
        </p>
      )}
      <div className="flex flex-wrap gap-2">
        {choice === 'approve_all' ? (
          // The server refuses this answer here: the exact payload is shown, and approved one operation at a time, on the task.
          <Button asChild>
            <Link to={`/orgs/${orgId}/tasks/${item.task_id}?tab=publishing`}>{t('inbox.openApproval')}</Link>
          </Button>
        ) : (
          <Button disabled={answer.isPending || sent || !choice || textMissing} onClick={submit}>
            {answer.isPending ? t('inbox.answering') : item.kind === 'approval' ? t('inbox.submitApproval') : t('inbox.answerResume')}
          </Button>
        )}
      </div>
      {RESOLVE_ONLY.includes(item.reason) && (
        <div className="flex flex-col gap-2 border-t pt-3">
          <p className="text-sm text-muted-foreground">{t('inbox.resolveHelp')}</p>
          <div className="flex flex-wrap gap-2">
            <Button
              variant="outline"
              disabled={resolve.isPending}
              onClick={() => resolve.mutate({ orgId, inbId: item.id, data: { action: 'resume' } }, { onSuccess: () => { renewResolve(); toast.success(t('inbox.answered')) }, onError })}
            >
              {t('inbox.resolveResume')}
            </Button>
            <Button
              variant="outline"
              disabled={resolve.isPending}
              onClick={() => resolve.mutate({ orgId, inbId: item.id, data: { action: 'cancel_run' } }, { onSuccess: () => { renewResolve(); toast.success(t('inbox.answered')) }, onError })}
            >
              {t('inbox.resolveCancel')}
            </Button>
          </div>
        </div>
      )}
    </div>
  )
}
