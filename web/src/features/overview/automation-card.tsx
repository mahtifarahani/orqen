import { PauseIcon, PlayIcon } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useAutomationState, usePutAutomation } from '@/api/hooks/automation'
import { LoadFailed, LoadingBlock } from '@/components/query-state'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { problemMessage } from '@/lib/problem-message'

/** The organization's pause switch: a paused organization polls nothing and holds every run at its next stage boundary. */
export function AutomationCard({ orgId }: { orgId: string }) {
  const { t } = useTranslation()
  const state = useAutomationState(orgId)
  const put = usePutAutomation(orgId, state.data?.revision ?? 0)
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('overviewPage.automationCard')}</CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-3 text-sm">
        {state.isPending && <LoadingBlock rows={1} />}
        {state.isError && <LoadFailed title={t('automation.loadFailed')} error={state.error} onRetry={() => void state.refetch()} />}
        {state.data && (
          <>
            {state.data.paused && (
              <Alert role="status">
                <AlertTitle>{t('automation.paused')}</AlertTitle>
                <AlertDescription>{t('automation.help')}</AlertDescription>
              </Alert>
            )}
            <Button
              className="self-start"
              variant={state.data.paused ? 'default' : 'outline'}
              disabled={put.isPending}
              onClick={() =>
                put.mutate({ orgId, data: { paused: !state.data.paused } }, { onError: (e) => toast.error(problemMessage(t, e)) })
              }
            >
              {state.data.paused ? <PlayIcon /> : <PauseIcon />}
              {state.data.paused ? t('automation.resume') : t('automation.pause')}
            </Button>
          </>
        )}
      </CardContent>
    </Card>
  )
}
