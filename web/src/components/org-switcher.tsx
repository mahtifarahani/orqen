import { useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate } from 'react-router'
import { CheckIcon, ChevronsUpDownIcon, PlusIcon } from 'lucide-react'
import { useOrgs } from '@/api/hooks/orgs'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { CreateOrgDialog } from '@/features/orgs/create-org-dialog'
import { setLastOrg } from '@/lib/last-org'

export function OrgSwitcher({ orgId }: { orgId: string }) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const orgs = useOrgs()
  const [createOpen, setCreateOpen] = useState(false)
  const triggerRef = useRef<HTMLButtonElement>(null)
  const current = orgs.data?.find((o) => o.id === orgId)

  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button ref={triggerRef} variant="ghost" aria-label={t('shell.switchOrg')} className="h-9 min-w-40 max-w-full justify-between gap-4 rounded-xl px-3">
            <span className="truncate font-semibold">{current?.name ?? t('shell.org')}</span>
            <ChevronsUpDownIcon className="opacity-60" />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start" className="min-w-56">
          <DropdownMenuLabel>{t('shell.org')}</DropdownMenuLabel>
          {orgs.data?.map((o) => (
            <DropdownMenuItem
              key={o.id}
              onSelect={() => {
                setLastOrg(o.id)
                navigate(`/orgs/${o.id}/overview`)
              }}
            >
              <span className="flex-1 truncate">{o.name}</span>
              {o.id === orgId && <CheckIcon />}
            </DropdownMenuItem>
          ))}
          <DropdownMenuSeparator />
          <DropdownMenuItem onSelect={() => setCreateOpen(true)}>
            <PlusIcon />
            {t('shell.createOrg')}
          </DropdownMenuItem>
          <DropdownMenuItem onSelect={() => navigate('/orgs')}>{t('shell.allOrgs')}</DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      <CreateOrgDialog open={createOpen} onOpenChange={setCreateOpen} returnFocusRef={triggerRef} />
    </>
  )
}
