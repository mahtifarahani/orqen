import { useTranslation } from 'react-i18next'
import { Navigate, Outlet } from 'react-router'
import { useAuthStatus } from '@/api/hooks/auth'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { problemMessage } from '@/lib/problem-message'

export function RequireSession() {
  const { t } = useTranslation()
  const status = useAuthStatus()

  if (status.isPending) {
    return (
      <div className="p-6" role="status" aria-label={t('app.loading')}>
        <Skeleton className="h-8 w-48" />
      </div>
    )
  }
  if (status.isError) {
    return (
      <div className="mx-auto max-w-md p-6">
        <Alert variant="destructive">
          <AlertTitle>{problemMessage(t, status.error)}</AlertTitle>
          <AlertDescription>
            <Button variant="outline" size="sm" onClick={() => void status.refetch()}>
              {t('common.retry')}
            </Button>
          </AlertDescription>
        </Alert>
      </div>
    )
  }
  if (!status.data.bootstrapped) return <Navigate to="/bootstrap" replace />
  if (!status.data.authenticated) return <Navigate to="/login" replace />
  return <Outlet />
}
