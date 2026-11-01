import { useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router'
import { PlusIcon } from 'lucide-react'
import { useOrgs } from '@/api/hooks/orgs'
import { Ltr } from '@/components/ltr'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { LocaleToggle } from '@/components/locale-toggle'
import { ThemeToggle } from '@/components/theme-toggle'
import { BrandLogo } from '@/components/brand-logo'
import { problemMessage } from '@/lib/problem-message'
import { setLastOrg } from '@/lib/last-org'
import { CreateOrgDialog } from './create-org-dialog'

export function OrgsPage() {
  const { t } = useTranslation()
  const orgs = useOrgs()
  const [open, setOpen] = useState(false)
  const createRef = useRef<HTMLButtonElement>(null)

  return (
    <div className="min-h-svh">
      <header className="flex items-center justify-between p-4">
        <BrandLogo />
        <div className="flex items-center gap-1">
          <LocaleToggle />
          <ThemeToggle />
        </div>
      </header>
      <main id="main" className="mx-auto flex max-w-3xl flex-col gap-6 p-4">
        <div className="flex flex-wrap items-start justify-between gap-4">
          <div>
            <h1 className="text-2xl font-semibold">{t('orgs.title')}</h1>
            <p className="text-sm text-muted-foreground">{t('orgs.subtitle')}</p>
          </div>
          <Button ref={createRef} onClick={() => setOpen(true)}>
            <PlusIcon />
            {t('orgs.create.title')}
          </Button>
        </div>

        {orgs.isPending && (
          <div className="flex flex-col gap-3" role="status" aria-label={t('app.loading')}>
            <Skeleton className="h-20 w-full" />
            <Skeleton className="h-20 w-full" />
          </div>
        )}
        {orgs.isError && (
          <Alert variant="destructive">
            <AlertTitle>{t('orgs.loadFailed')}</AlertTitle>
            <AlertDescription className="flex flex-col items-start gap-2">
              {problemMessage(t, orgs.error)}
              <Button size="sm" variant="outline" onClick={() => void orgs.refetch()}>
                {t('common.retry')}
              </Button>
            </AlertDescription>
          </Alert>
        )}
        {orgs.data?.length === 0 && (
          <Card>
            <CardContent className="py-8 text-center text-sm text-muted-foreground">{t('orgs.empty')}</CardContent>
          </Card>
        )}
        <ul className="flex flex-col gap-3">
          {orgs.data?.map((org) => (
            <li key={org.id}>
              <Card>
                <CardHeader className="flex-row items-center justify-between gap-4">
                  <div className="flex flex-col gap-1">
                    <CardTitle>{org.name}</CardTitle>
                    <CardDescription>
                      <Ltr>{org.slug}</Ltr>
                      {' · '}
                      {t(`locale.${org.locale}`)}
                      {' · '}
                      <Ltr>{org.timezone}</Ltr>
                    </CardDescription>
                  </div>
                  <Button asChild variant="outline" size="sm">
                    <Link to={`/orgs/${org.id}/overview`} onClick={() => setLastOrg(org.id)}>
                      {t('orgs.open')}
                    </Link>
                  </Button>
                </CardHeader>
              </Card>
            </li>
          ))}
        </ul>
      </main>
      <CreateOrgDialog open={open} onOpenChange={setOpen} returnFocusRef={createRef} />
    </div>
  )
}
