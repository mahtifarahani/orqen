import { useTranslation } from 'react-i18next'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { problemMessage } from '@/lib/problem-message'

/** Loading placeholder with an accessible status label. */
export function LoadingBlock({ rows = 3 }: { rows?: number }) {
  const { t } = useTranslation()
  return (
    <div className="flex flex-col gap-2" role="status" aria-label={t('app.loading')}>
      {Array.from({ length: rows }, (_, i) => (
        <Skeleton key={i} className="h-10 w-full" />
      ))}
    </div>
  )
}

/** Failed state: the problem message plus a Retry button. */
export function LoadFailed({ title, error, onRetry }: { title: string; error: unknown; onRetry?: () => void }) {
  const { t } = useTranslation()
  return (
    <Alert variant="destructive">
      <AlertTitle>{title}</AlertTitle>
      <AlertDescription className="flex flex-col items-start gap-2">
        {problemMessage(t, error)}
        {onRetry && (
          <Button size="sm" variant="outline" onClick={onRetry}>
            {t('common.retry')}
          </Button>
        )}
      </AlertDescription>
    </Alert>
  )
}
