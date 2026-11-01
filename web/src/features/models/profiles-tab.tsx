import { useEffect, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { Link, useNavigate } from 'react-router'
import { DownloadIcon, PlusIcon } from 'lucide-react'
import { toast } from 'sonner'
import { usePersonalProfileNames } from '@/api/hooks/catherd'
import { useCreateProfile, useProfiles, useStartImport } from '@/api/hooks/models'
import { newIdempotencyKey } from '@/api/idempotency'
import { ApiProblem, ok } from '@/api/problem'
import { Field } from '@/components/field'
import { Ltr } from '@/components/ltr'
import { LoadFailed, LoadingBlock } from '@/components/query-state'
import { useOperationRunner } from '@/components/use-operation-runner'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { formatDateTime } from '@/lib/format'
import { problemMessage } from '@/lib/problem-message'
import { useLocale } from '@/lib/use-locale'

const slugOf = (name: string) =>
  name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 24)

export function ProfilesTab({ orgId, onOpenProfile }: { orgId: string; onOpenProfile?: (id: string) => void }) {
  const { t } = useTranslation()
  const locale = useLocale()
  const navigate = useNavigate()
  const profiles = useProfiles(orgId)
  const [dialog, setDialog] = useState<'create' | 'import' | null>(null)
  const open = (id: string) => (onOpenProfile ? onOpenProfile(id) : navigate(`/orgs/${orgId}/models/profiles/${id}`))
  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap gap-2">
        <Button onClick={() => setDialog('create')}>
          <PlusIcon />
          {t('profiles.create')}
        </Button>
        <Button variant="outline" onClick={() => setDialog('import')}>
          <DownloadIcon />
          {t('profiles.import')}
        </Button>
      </div>
      {profiles.isPending && <LoadingBlock />}
      {profiles.isError && <LoadFailed title={t('models.loadFailed')} error={profiles.error} onRetry={() => void profiles.refetch()} />}
      {profiles.isSuccess && profiles.data.length === 0 && (
        <Card>
          <CardContent className="flex flex-col items-start gap-3 py-6">
            <p className="max-w-prose text-sm text-muted-foreground">{t('profiles.empty')}</p>
          </CardContent>
        </Card>
      )}
      {profiles.isSuccess && profiles.data.length > 0 && (
        <div className="overflow-x-auto rounded-lg border">
          <Table aria-label={t('models.tabs.profiles')}>
            <TableHeader>
              <TableRow>
                <TableHead>{t('profiles.columns.name')}</TableHead>
                <TableHead>{t('profiles.columns.slug')}</TableHead>
                <TableHead>{t('profiles.columns.source')}</TableHead>
                <TableHead>{t('profiles.columns.validation')}</TableHead>
                <TableHead>{t('profiles.columns.updated')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {profiles.data.map((p) => (
                <TableRow key={p.id}>
                  <TableCell className="font-medium">
                    <Link to={`/orgs/${orgId}/models/profiles/${p.id}`} onClick={(e) => { if (onOpenProfile) { e.preventDefault(); open(p.id) } }} className="underline-offset-4 outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring">
                      {p.name}
                    </Link>
                  </TableCell>
                  <TableCell>
                    <Ltr>{p.slug}</Ltr>
                  </TableCell>
                  <TableCell>
                    <Badge variant="outline">{t(`profiles.sources.${p.source.kind}`, { defaultValue: p.source.kind })}</Badge>
                  </TableCell>
                  <TableCell>
                    {p.latest_version_id ? <Badge>{t('profiles.hasVersion')}</Badge> : <Badge variant="outline">{t('profiles.noVersion')}</Badge>}
                    {p.last_validation && <Badge variant={p.last_validation.valid ? 'secondary' : 'destructive'} className="ms-1">{p.last_validation.valid ? t('profile.validation.valid') : t('profile.validation.invalid')}</Badge>}
                  </TableCell>
                  <TableCell className="text-xs">{formatDateTime(p.updated_at, locale)}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}
      {dialog === 'create' && <CreateDialog orgId={orgId} onClose={() => setDialog(null)} onCreated={open} />}
      {dialog === 'import' && <ImportDialog orgId={orgId} onClose={() => setDialog(null)} onImported={open} />}
    </div>
  )
}

function CreateDialog({ orgId, onClose, onCreated }: { orgId: string; onClose: () => void; onCreated: (id: string) => void }) {
  const { t } = useTranslation()
  const [key] = useState(newIdempotencyKey)
  const create = useCreateProfile(orgId, key)
  const [name, setName] = useState('')
  const [slug, setSlug] = useState('')
  const [touched, setTouched] = useState(false)
  const [description, setDescription] = useState('')
  const errors = create.error instanceof ApiProblem ? create.error.fieldErrors() : {}
  const submit = (e: FormEvent) => {
    e.preventDefault()
    create.mutate(
      { orgId, data: { name: name.trim(), slug: slug.trim(), description: description.trim() || null, from: { kind: 'blank' } } },
      {
        onSuccess: (res) => {
          toast.success(t('profiles.created'))
          onClose()
          onCreated(ok(res).id)
        },
      },
    )
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('profiles.create')}</DialogTitle>
          <DialogDescription>{t('profiles.createHelp')}</DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} className="flex flex-col gap-4">
          <Field id="mp-name" label={t('profiles.fields.name')} error={errors.name}>
            {(p) => (
              <Input
                {...p}
                required
                value={name}
                onChange={(e) => {
                  setName(e.target.value)
                  if (!touched) setSlug(slugOf(e.target.value))
                }}
              />
            )}
          </Field>
          <Field id="mp-slug" label={t('profiles.fields.slug')} hint={t('profiles.fields.slugHint')} error={errors.slug}>
            {(p) => (
              <Input
                {...p}
                required
                dir="ltr"
                value={slug}
                onChange={(e) => {
                  setTouched(true)
                  setSlug(e.target.value)
                }}
              />
            )}
          </Field>
          <Field id="mp-desc" label={t('profiles.fields.description')}>
            {(p) => <Input {...p} value={description} onChange={(e) => setDescription(e.target.value)} />}
          </Field>
          {create.isError && Object.keys(errors).length === 0 && (
            <Alert variant="destructive" role="alert">
              <AlertDescription>{problemMessage(t, create.error)}</AlertDescription>
            </Alert>
          )}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" disabled={create.isPending || !name.trim() || !slug.trim()}>
              {t('common.create')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function ImportDialog({ orgId, onClose, onImported }: { orgId: string; onClose: () => void; onImported: (id: string) => void }) {
  const { t } = useTranslation()
  const personal = usePersonalProfileNames(orgId)
  const runner = useOperationRunner(orgId)
  const start = useStartImport(runner.key)
  const [pick, setPick] = useState<string>()
  const [name, setName] = useState('')
  const [slug, setSlug] = useState('')
  const result = runner.operation?.result as { profile_id?: string } | null | undefined
  useEffect(() => {
    if (runner.succeeded && result?.profile_id) {
      toast.success(t('profiles.imported'))
      onClose()
      onImported(result.profile_id)
    }
  })
  const errors = start.error instanceof ApiProblem ? start.error.fieldErrors() : {}
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('profiles.import')}</DialogTitle>
          <DialogDescription>{t('profiles.importNote')}</DialogDescription>
        </DialogHeader>
        {personal.isPending && <LoadingBlock rows={2} />}
        {personal.isError && <LoadFailed title={t('profiles.personalFailed')} error={personal.error} onRetry={() => void personal.refetch()} />}
        {personal.isSuccess && !personal.data.available && <Alert><AlertDescription>{t('profiles.personalUnavailable')}</AlertDescription></Alert>}
        {personal.isSuccess && personal.data.available && personal.data.profiles.length === 0 && <p className="text-sm text-muted-foreground">{t('profiles.personalEmpty')}</p>}
        {personal.isSuccess && personal.data.profiles.length > 0 && (
          <form
            className="flex flex-col gap-4"
            onSubmit={(e) => {
              e.preventDefault()
              if (pick) start.mutate({ orgId, data: { personal_name: pick, name: name.trim(), slug: slug.trim() } }, { onSuccess: runner.started })
            }}
          >
            <Field id="imp-pick" label={t('profiles.personalName')}>
              {(p) => (
                <Select
                  value={pick}
                  onValueChange={(v) => {
                    setPick(v)
                    if (!name) setName(v)
                    if (!slug) setSlug(slugOf(v))
                  }}
                >
                  <SelectTrigger {...p} className="w-full">
                    <SelectValue placeholder={t('profiles.choosePersonal')} />
                  </SelectTrigger>
                  <SelectContent>
                    {personal.data.profiles.map((pp) => (
                      <SelectItem key={pp.name} value={pp.name}>
                        {pp.name}
                        {pp.active ? ` (${t('profiles.active')})` : ''}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              )}
            </Field>
            <Field id="imp-name" label={t('profiles.fields.name')} error={errors.name}>
              {(p) => <Input {...p} required value={name} onChange={(e) => setName(e.target.value)} />}
            </Field>
            <Field id="imp-slug" label={t('profiles.fields.slug')} hint={t('profiles.fields.slugHint')} error={errors.slug}>
              {(p) => <Input {...p} required dir="ltr" value={slug} onChange={(e) => setSlug(e.target.value)} />}
            </Field>
            {start.isError && Object.keys(errors).length === 0 && <Alert variant="destructive" role="alert"><AlertDescription>{problemMessage(t, start.error)}</AlertDescription></Alert>}
            {runner.failed && <Alert variant="destructive" role="alert"><AlertDescription>{t('profiles.importFailed')}</AlertDescription></Alert>}
            <DialogFooter>
              <Button type="button" variant="outline" onClick={onClose}>
                {t('common.cancel')}
              </Button>
              <Button type="submit" disabled={!pick || !name.trim() || !slug.trim() || start.isPending || runner.running}>
                {runner.running ? t('profiles.importing') : t('profiles.importAction')}
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}
