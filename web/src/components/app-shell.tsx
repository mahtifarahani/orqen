import { IconButton } from '@/components/icon-button'
import { useEffect } from 'react'
import { useTranslation } from 'react-i18next'
import { Link, Outlet, useNavigate, useParams } from 'react-router'
import { HeartPulseIcon, LogOutIcon } from 'lucide-react'
import { useOrg } from '@/api/hooks/orgs'
import { useLogout } from '@/api/hooks/auth'
import { ApiProblem } from '@/api/problem'
import { useOrgEvents } from '@/api/sse'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { TooltipProvider } from '@/components/ui/tooltip'
import { applyLocale, storedLocale } from '@/i18n/direction'
import { setLastOrg } from '@/lib/last-org'
import { WriteModeBadge } from '@/features/publishing/write-mode-badge'
import { EventsContext } from './events-context'
import { LiveIndicator } from './live-indicator'
import { LocaleToggle } from './locale-toggle'
import { OrgSwitcher } from './org-switcher'
import { SidebarNav } from './sidebar-nav'
import { ThemeToggle } from './theme-toggle'
import { NotFoundPage } from './not-found'
import { BrandLogo } from './brand-logo'

export function AppShell() {
  const { t } = useTranslation()
  const { orgId = '' } = useParams()
  const navigate = useNavigate()
  const org = useOrg(orgId)
  const feed = useOrgEvents(org.data ? orgId : undefined)
  const logout = useLogout()

  const orgLocale = org.data?.locale
  useEffect(() => {
    if (!org.data) return
    setLastOrg(org.data.id)
    // The org's locale applies unless the user chose a language themselves (D28).
    if (!storedLocale() && orgLocale) applyLocale(orgLocale)
  }, [org.data, orgLocale])

  if (org.isError && org.error instanceof ApiProblem && org.error.status === 404) return <NotFoundPage />

  return (
    <TooltipProvider>
      <EventsContext.Provider value={feed}>
        <a
          href="#main"
          className="sr-only focus:not-sr-only focus:fixed focus:start-2 focus:top-2 focus:z-50 focus:rounded-md focus:bg-background focus:px-3 focus:py-2 focus:ring-2 focus:ring-ring"
        >
          {t('shell.skipToContent')}
        </a>
        <div className="app-shell flex min-h-svh flex-col md:flex-row">
          <aside className="sidebar-material flex items-center gap-2 border-b p-3 text-sidebar-foreground md:sticky md:top-0 md:block md:h-svh md:w-64 md:shrink-0 md:self-start md:overflow-y-auto md:border-b-0 md:border-e md:p-4">
            <Link to="/orgs" className="block shrink-0 rounded-xl px-2 py-2 text-lg font-semibold outline-none focus-visible:ring-2 focus-visible:ring-ring md:mb-6 md:px-3 md:py-3">
              <BrandLogo compact className="md:hidden" />
              <BrandLogo className="hidden md:block" />
            </Link>
            <SidebarNav orgId={orgId} />
          </aside>
          <div className="flex min-w-0 flex-1 flex-col">
            <header className="toolbar-material sticky top-0 z-20 flex min-h-16 flex-wrap items-center justify-between gap-2 px-3 py-2 md:px-5">
              <OrgSwitcher orgId={orgId} />
              <div className="flex flex-wrap items-center gap-1.5">
                <Button asChild variant="ghost" size="sm">
                  <Link to={`/orgs/${orgId}/health`}>
                    <HeartPulseIcon />
                    {t('health.link')}
                  </Link>
                </Button>
                <WriteModeBadge />
                <LiveIndicator />
                <LocaleToggle />
                <ThemeToggle />
                <IconButton
                  variant="ghost"
                  size="icon"
                  aria-label={t('auth.logout')}
                  onClick={() => logout.mutate(undefined, { onSuccess: () => navigate('/login') })}
                >
                  <LogOutIcon className="rtl:-scale-x-100" />
                </IconButton>
              </div>
            </header>
            <main id="main" tabIndex={-1} className="flex-1 p-4 outline-none md:p-8 lg:p-10">
              {org.isPending ? <Skeleton className="h-8 w-64" /> : <Outlet />}
            </main>
          </div>
        </div>
      </EventsContext.Provider>
    </TooltipProvider>
  )
}
