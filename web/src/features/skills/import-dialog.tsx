import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { newIdempotencyKey } from '@/api/idempotency'
import { useImportSkillRun } from '@/api/hooks/skills'
import { Field } from '@/components/field'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { problemMessage } from '@/lib/problem-message'

/** Imports one SKILL.md. The text is linted by the server and lands as a draft that still needs approval. */
export function ImportDialog({ orgId, open, onOpenChange, onImported }: { orgId: string; open: boolean; onOpenChange: (o: boolean) => void; onImported: (skillId: string) => void }) {
  const { t } = useTranslation()
  const [key, setKey] = useState(newIdempotencyKey)
  const [filename, setFilename] = useState('SKILL.md')
  const [content, setContent] = useState('')
  const imp = useImportSkillRun(orgId, key)
  const readFile = async (f: File | undefined) => {
    if (!f) return
    setFilename(f.name)
    setContent(await f.text())
  }
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{t('skills.import.title')}</DialogTitle>
          <DialogDescription>{t('skills.import.help')}</DialogDescription>
        </DialogHeader>
        <form
          className="flex flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault()
            imp.mutate(
              { orgId, data: { filename: filename.trim(), content } },
              {
                onSuccess: (res) => {
                  setKey(newIdempotencyKey())
                  setContent('')
                  toast.success(t('skills.import.done'))
                  onOpenChange(false)
                  // the answer is the new draft version; its skill_id opens the review sheet
                  const d = res.data as { skill_id?: string }
                  if (d.skill_id) onImported(d.skill_id)
                },
              },
            )
          }}
        >
          <Field id="skill-file" label={t('skills.import.file')}>
            {(f) => <Input {...f} type="file" accept=".md,text/markdown,text/plain" onChange={(e) => void readFile(e.target.files?.[0])} />}
          </Field>
          <Field id="skill-filename" label={t('skills.import.filename')}>
            {(f) => <Input {...f} dir="ltr" value={filename} onChange={(e) => setFilename(e.target.value)} />}
          </Field>
          <Field id="skill-content" label={t('skills.import.content')} hint={t('skills.import.contentHint')}>
            {(f) => <Textarea {...f} dir="ltr" rows={10} className="font-mono text-xs" value={content} onChange={(e) => setContent(e.target.value)} />}
          </Field>
          {imp.isError && (
            <Alert variant="destructive" role="alert">
              <AlertDescription>{problemMessage(t, imp.error)}</AlertDescription>
            </Alert>
          )}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" disabled={!content.trim() || !filename.trim() || imp.isPending}>
              {t('skills.import.submit')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
