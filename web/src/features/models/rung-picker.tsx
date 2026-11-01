import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useCatalog } from '@/api/hooks/catherd'
import { Ltr } from '@/components/ltr'
import { LoadFailed, LoadingBlock } from '@/components/query-state'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { RungChips } from './rung-chip'
import { rungAddable, rungBlockedReason } from './rung-rules'

/** Search list over the catalog, filtered by role. Rungs that cannot be used are listed but disabled, with the reason. */
export function RungPicker({ orgId, role, existing, onAdd, onClose }: { orgId: string; role: string; existing: string[]; onAdd: (rung: string) => void; onClose: () => void }) {
  const { t } = useTranslation()
  const [text, setText] = useState('')
  const [query, setQuery] = useState('')
  useEffect(() => {
    const id = setTimeout(() => setQuery(text.trim()), 250)
    return () => clearTimeout(id)
  }, [text])
  const catalog = useCatalog(orgId, { role, ...(query ? { text: query } : {}) })
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[85vh] max-w-3xl overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{t('profile.roles.addRungTitle', { role: t(`profile.roleNames.${role}`, { defaultValue: role }) })}</DialogTitle>
          <DialogDescription>{t('profile.roles.addRungHelp')}</DialogDescription>
        </DialogHeader>
        <Input aria-label={t('catalog.search')} type="search" value={text} onChange={(e) => setText(e.target.value)} placeholder={t('catalog.searchPlaceholder')} />
        {catalog.isPending && <LoadingBlock />}
        {catalog.isError && <LoadFailed title={t('catalog.loadFailed')} error={catalog.error} onRetry={() => void catalog.refetch()} />}
        {catalog.isSuccess && !catalog.data.refreshed_at && <p className="text-sm text-muted-foreground">{t('catalog.never')}</p>}
        {catalog.isSuccess && catalog.data.refreshed_at && catalog.data.models.length === 0 && <p className="text-sm text-muted-foreground">{t('catalog.noMatches')}</p>}
        <ul className="flex flex-col gap-4">
          {catalog.data?.models.map((m) => (
            <li key={m.family_id} className="rounded-lg border p-3">
              <div className="mb-2 flex flex-wrap items-baseline gap-2">
                <span className="font-medium">
                  <Ltr className="font-sans">{m.name ?? m.model}</Ltr>
                </span>
                <Ltr className="text-xs text-muted-foreground">{m.backend}</Ltr>
              </div>
              <ul className="flex flex-col divide-y">
                {m.rungs.map((r) => {
                  const reason = rungBlockedReason(r, t)
                  const taken = existing.includes(r.rung)
                  const reasonId = `rung-${r.rung}-why`
                  return (
                    <li key={r.rung} className="flex flex-wrap items-center justify-between gap-2 py-2">
                      <div className="flex min-w-0 flex-col gap-1">
                        <Ltr className="break-all text-xs">{r.rung}</Ltr>
                        <RungChips rung={r} />
                        {reason && (
                          <span id={reasonId} className="text-xs text-muted-foreground">
                            {reason}
                            {r.reasons.length > 0 && <> · {r.reasons.join(', ')}</>}
                          </span>
                        )}
                      </div>
                      <Button
                        type="button"
                        size="sm"
                        variant="outline"
                        disabled={!rungAddable(r) || taken}
                        aria-describedby={reason ? reasonId : undefined}
                        onClick={() => {
                          onAdd(r.rung)
                          onClose()
                        }}
                      >
                        {taken ? t('profile.roles.rungAdded') : t('profile.roles.addRung')}
                      </Button>
                    </li>
                  )
                })}
              </ul>
            </li>
          ))}
        </ul>
      </DialogContent>
    </Dialog>
  )
}
