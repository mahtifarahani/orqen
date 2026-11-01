import { useTranslation } from 'react-i18next'
import { useParams } from 'react-router'
import { useRunSecurityFindings } from '@/api/hooks/security'
import { LoadingBlock } from '@/components/query-state'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { SecurityFindings } from './findings-list'

/** The security findings behind the merge request's security check (only rendered when that check exists). */
export function MergeRequestSecurity({ runId }: { runId: string }) {
  const { t } = useTranslation()
  const { orgId = '' } = useParams()
  const q = useRunSecurityFindings(orgId, runId)
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('security.findings.title')}</CardTitle>
      </CardHeader>
      <CardContent>
        {q.isPending && <LoadingBlock rows={1} />}
        {q.data && <SecurityFindings orgId={orgId} findings={q.data} />}
      </CardContent>
    </Card>
  )
}
