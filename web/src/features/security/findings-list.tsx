import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import type { SecurityFindingDto } from '@/api/generated/model'
import { useIssueDraft, useWaiveSecurity } from '@/api/hooks/security'
import { Field } from '@/components/field'
import { Ltr } from '@/components/ltr'
import { LoadingBlock } from '@/components/query-state'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Textarea } from '@/components/ui/textarea'
import { problemMessage } from '@/lib/problem-message'

const severityVariant = (s: string): 'destructive' | 'secondary' | 'outline' => (s === 'blocker' ? 'destructive' : s === 'major' ? 'secondary' : 'outline')

/** Security findings of a run or a scan. A blocking open finding can be waived with a written reason. */
export function SecurityFindings({ orgId, findings }: { orgId: string; findings: SecurityFindingDto[] }) {
  const { t } = useTranslation()
  const [waiving, setWaiving] = useState<SecurityFindingDto | null>(null)
  const [drafting, setDrafting] = useState<string | null>(null)
  if (findings.length === 0) return <p className="text-sm text-muted-foreground">{t('security.findings.none')}</p>
  return (
    <>
      <ul className="flex flex-col gap-2" aria-label={t('security.findings.title')}>
        {findings.map((f) => (
          <li key={f.id} className="flex flex-col gap-2 rounded-md border p-3 text-sm" data-finding-status={f.status}>
            <div className="flex flex-wrap items-center gap-2">
              <Badge variant={severityVariant(f.severity)}>{t(`security.severity.${f.severity}`)}</Badge>
              {f.blocking && <Badge variant="destructive">{t('security.findings.blocking')}</Badge>}
              <Badge variant="outline">{t(`security.findings.status.${f.status}`)}</Badge>
              {f.cwe && <Ltr className="text-xs">{f.cwe}</Ltr>}
            </div>
            <p className="font-medium" dir="auto">{f.title}</p>
            <p className="text-muted-foreground">
              <Ltr className="break-all">{f.file}{f.line > 0 ? `:${f.line}` : ''}</Ltr>
              {!f.location_valid && <span className="ms-2 text-xs">{t('security.findings.locationUnverified')}</span>}
            </p>
            {f.body && <p className="whitespace-pre-wrap" dir="auto">{f.body}</p>}
            <div className="flex flex-wrap gap-2">
              {f.status === 'open' && f.blocking && (
                <Button size="sm" variant="outline" onClick={() => setWaiving(f)}>
                  {t('security.waive.action')}
                </Button>
              )}
              <Button size="sm" variant="ghost" onClick={() => setDrafting(f.id)}>
                {t('security.draft.action')}
              </Button>
            </div>
          </li>
        ))}
      </ul>
      {waiving && <WaiveDialog orgId={orgId} finding={waiving} onClose={() => setWaiving(null)} />}
      {drafting && <DraftDialog orgId={orgId} id={drafting} onClose={() => setDrafting(null)} />}
    </>
  )
}

function WaiveDialog({ orgId, finding, onClose }: { orgId: string; finding: SecurityFindingDto; onClose: () => void }) {
  const { t } = useTranslation()
  const waive = useWaiveSecurity(orgId, finding.revision)
  const [text, setText] = useState('')
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('security.waive.title')}</DialogTitle>
          <DialogDescription dir="auto">{finding.title}</DialogDescription>
        </DialogHeader>
        <form
          className="flex flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault()
            waive.mutate({ orgId, sfdId: finding.id, data: { text: text.trim() } }, { onSuccess: () => { toast.success(t('security.waive.done')); onClose() } })
          }}
        >
          <Field id="waive-text" label={t('security.waive.reason')} hint={t('security.waive.hint')}>
            {(f) => <Textarea {...f} rows={4} maxLength={2000} value={text} onChange={(e) => setText(e.target.value)} />}
          </Field>
          {waive.isError && (
            <Alert variant="destructive" role="alert">
              <AlertDescription>{problemMessage(t, waive.error)}</AlertDescription>
            </Alert>
          )}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>{t('common.cancel')}</Button>
            <Button type="submit" disabled={!text.trim() || waive.isPending}>{t('security.waive.submit')}</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/** The Markdown text of an issue for this finding. Orqen only shows it: it never creates the issue. */
function DraftDialog({ orgId, id, onClose }: { orgId: string; id: string; onClose: () => void }) {
  const { t } = useTranslation()
  const q = useIssueDraft(orgId, id)
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{t('security.draft.title')}</DialogTitle>
          <DialogDescription>{t('security.draft.help')}</DialogDescription>
        </DialogHeader>
        {q.isPending && <LoadingBlock rows={2} />}
        {q.isError && <p role="alert" className="text-sm text-destructive">{problemMessage(t, q.error)}</p>}
        {q.data && <pre dir="auto" className="max-h-80 overflow-auto whitespace-pre-wrap rounded-md bg-muted p-3 text-xs">{q.data.markdown}</pre>}
        <DialogFooter>
          {q.data && (
            <Button variant="outline" onClick={() => void navigator.clipboard?.writeText(q.data.markdown).then(() => toast.success(t('security.draft.copied')))}>
              {t('security.draft.copy')}
            </Button>
          )}
          <Button onClick={onClose}>{t('common.close')}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
