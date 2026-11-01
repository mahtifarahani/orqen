import type { ComponentType } from 'react'
import { useTranslation } from 'react-i18next'
import { NavLink } from 'react-router'
import {
  FolderKanbanIcon,
  GitMergeIcon,
  InboxIcon,
  LayoutDashboardIcon,
  ListChecksIcon,
  NetworkIcon,
  BrainCircuitIcon,
  PlugIcon,
  PuzzleIcon,
  BookOpenIcon,
  ShieldIcon,
  SettingsIcon,
} from 'lucide-react'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { cn } from '@/lib/utils'
import { useLocale } from '@/lib/use-locale'
import { useTaskSummary } from '@/api/hooks/tasks'
import { formatNumber } from '@/lib/format'

interface Item {
  key: string
  icon: ComponentType<{ className?: string }>
  to?: string
}

const ITEMS: Item[] = [
  { key: 'overview', icon: LayoutDashboardIcon, to: 'overview' },
  { key: 'projects', icon: FolderKanbanIcon, to: 'projects' },
  { key: 'tasks', icon: ListChecksIcon, to: 'tasks' },
  { key: 'inbox', icon: InboxIcon, to: 'inbox' },
  { key: 'mergeRequests', icon: GitMergeIcon, to: 'merge-requests' },
  { key: 'knowledge', icon: BookOpenIcon, to: 'knowledge' },
  { key: 'graph', icon: NetworkIcon, to: 'graph' },
  { key: 'models', icon: BrainCircuitIcon, to: 'models' },
  { key: 'integrations', icon: PlugIcon, to: 'connections' },
  { key: 'extensions', icon: PuzzleIcon, to: 'extensions' },
  { key: 'security', icon: ShieldIcon, to: 'security' },
  { key: 'settings', icon: SettingsIcon, to: 'settings' },
]

const base = 'flex min-h-10 shrink-0 items-center gap-3 whitespace-nowrap rounded-xl px-3 py-2 text-sm font-medium transition-colors duration-150 outline-none focus-visible:ring-2 focus-visible:ring-ring [&>svg]:shrink-0'

export function SidebarNav({ orgId }: { orgId: string }) {
  const { t } = useTranslation()
  const locale = useLocale()
  const side = locale === 'fa' ? 'left' : 'right' // toward the content, away from the sidebar
  const summary = useTaskSummary(orgId).data
  const open = summary?.inbox_open ?? 0
  const mrReady = summary?.merge_requests?.ready ?? 0
  return (
    <nav aria-label={t('nav.label')} className="flex min-w-0 gap-1 overflow-x-auto p-1 md:flex-col md:overflow-visible md:p-0">
      {ITEMS.map(({ key, icon: Icon, to }) =>
        to ? (
          <NavLink
            key={key}
            to={`/orgs/${orgId}/${to}`}
            className={({ isActive }) =>
              cn(base, isActive ? 'bg-sidebar-accent text-sidebar-accent-foreground shadow-sm' : 'hover:bg-foreground/5 active:bg-foreground/10')
            }
          >
            <Icon className="size-4" />
            {t(`nav.${key}`)}
            {key === 'inbox' && open > 0 && (
              <span className="ms-auto rounded-full bg-primary px-1.5 text-xs text-primary-foreground tabular-nums" aria-label={t('inbox.openCount', { count: open })}>
                {formatNumber(open, locale)}
              </span>
            )}
            {key === 'mergeRequests' && mrReady > 0 && (
              <span className="ms-auto rounded-full bg-primary px-1.5 text-xs text-primary-foreground tabular-nums" aria-label={t('mrPage.readyCount', { count: mrReady })}>
                {formatNumber(mrReady, locale)}
              </span>
            )}
          </NavLink>
        ) : (
          <Tooltip key={key}>
            <TooltipTrigger asChild>
              <span role="link" tabIndex={0} aria-disabled="true" className={cn(base, 'cursor-not-allowed text-muted-foreground opacity-60')}>
                <Icon className="size-4" />
                {t(`nav.${key}`)}
              </span>
            </TooltipTrigger>
            <TooltipContent side={side}>{t('nav.notAvailable')}</TooltipContent>
          </Tooltip>
        ),
      )}
    </nav>
  )
}
