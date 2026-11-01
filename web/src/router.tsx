import { createBrowserRouter, Navigate } from 'react-router'
import { AppShell } from '@/components/app-shell'
import { NotFoundPage } from '@/components/not-found'
import { BootstrapPage, LoginPage } from '@/features/auth/claim-token-page'
import { RequireSession } from '@/features/auth/require-session'
import { ConnectionsPage } from '@/features/connections/connections-page'
import { HomeRedirect } from '@/features/home-redirect'
import { OrgSettingsPage } from '@/features/orgs/org-settings-page'
import { OrgsPage } from '@/features/orgs/orgs-page'
import { OverviewPage } from '@/features/overview/overview-page'
import { DiscoverPage } from '@/features/projects/discover-page'
import { ProjectDetailPage } from '@/features/projects/project-detail-page'
import { ProjectsPage } from '@/features/projects/projects-page'
import { MergeRequestsPage } from '@/features/merge-requests/merge-requests-page'
import { InboxPage } from '@/features/inbox/inbox-page'
import { TaskDetailPage } from '@/features/tasks/task-detail-page'
import { TasksPage } from '@/features/tasks/tasks-page'
import { ExtensionsPage } from '@/features/extensions/extensions-page'
import { SecurityPage } from '@/features/security/security-page'
import { GraphPage } from '@/features/graph/graph-page'
import { KnowledgePage } from '@/features/knowledge/knowledge-page'
import { HealthPage } from '@/features/health/health-page'
import { NewOrgStep } from '@/features/setup/new-org-step'
import { SetupPage } from '@/features/setup/setup-page'
import { ModelsPage } from '@/features/models/models-page'
import { ProfileEditorPage } from '@/features/models/profile-editor-page'

export const router = createBrowserRouter([
  { path: '/bootstrap', element: <BootstrapPage /> },
  { path: '/login', element: <LoginPage /> },
  {
    element: <RequireSession />,
    children: [
      { path: '/', element: <HomeRedirect /> },
      { path: '/orgs', element: <OrgsPage /> },
      { path: '/setup/new', element: <NewOrgStep /> },
      {
        path: '/orgs/:orgId',
        element: <AppShell />,
        children: [
          { index: true, element: <Navigate to="overview" replace /> },
          { path: 'overview', element: <OverviewPage /> },
          { path: 'connections', element: <ConnectionsPage /> },
          { path: 'projects', element: <ProjectsPage /> },
          { path: 'projects/discover', element: <DiscoverPage /> },
          { path: 'projects/:projectId', element: <ProjectDetailPage /> },
          { path: 'tasks', element: <TasksPage /> },
          { path: 'tasks/:taskId', element: <TaskDetailPage /> },
          { path: 'inbox', element: <InboxPage /> },
          { path: 'merge-requests', element: <MergeRequestsPage /> },
          { path: 'knowledge', element: <KnowledgePage /> },
          { path: 'graph', element: <GraphPage /> },
          { path: 'models', element: <ModelsPage /> },
          { path: 'models/profiles/:profileId', element: <ProfileEditorPage /> },
          { path: 'extensions', element: <ExtensionsPage /> },
          { path: 'security', element: <SecurityPage /> },
          { path: 'setup', element: <SetupPage /> },
          { path: 'health', element: <HealthPage /> },
          { path: 'settings', element: <OrgSettingsPage /> },
        ],
      },
    ],
  },
  { path: '*', element: <NotFoundPage /> },
])
