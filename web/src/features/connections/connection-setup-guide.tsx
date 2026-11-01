import { useTranslation } from 'react-i18next'
import type { AuthMethod, Deployment, Provider } from '@/api/generated/model'
import { Alert, AlertDescription } from '@/components/ui/alert'

const DOCS = {
  atlassianCloud: 'https://support.atlassian.com/atlassian-account/docs/manage-api-tokens-for-your-atlassian-account/',
  atlassianPat: 'https://confluence.atlassian.com/enterprise/using-personal-access-tokens-1026032365.html',
  gitlab: 'https://docs.gitlab.com/user/profile/personal_access_tokens/',
  figma: 'https://developers.figma.com/docs/rest-api/personal-access-tokens/',
}

export function ConnectionSetupGuide({ provider, deployment, authMethod }: {
  provider: Provider
  deployment: Deployment
  authMethod: AuthMethod
}) {
  const { t } = useTranslation()
  const adapterAvailable = provider === 'jira' || provider === 'gitlab'
  const authSupported = provider === 'gitlab'
    ? authMethod !== 'basic'
    : provider === 'jira' && !(deployment === 'cloud' && authMethod === 'personal_access_token')
  const tokenSource = provider === 'gitlab' || provider === 'figma'
    ? provider
    : deployment === 'cloud' ? 'atlassianCloud' : 'atlassianPat'
  const oauthDocs = provider === 'gitlab'
    ? 'https://docs.gitlab.com/integration/oauth_provider/'
    : provider === 'figma'
      ? 'https://developers.figma.com/docs/rest-api/oauth-apps/'
      : deployment === 'cloud'
        ? 'https://developer.atlassian.com/cloud/jira/platform/oauth-2-3lo-apps/'
        : undefined
  const docs = authMethod === 'oauth' ? oauthDocs : authMethod === 'basic' ? undefined : DOCS[tokenSource]
  const source = authMethod === 'oauth' ? 'oauthSource' : authMethod === 'basic' ? 'basicSource' : tokenSource

  return (
    <section aria-label={t('connections.form.guide.title')} className="min-w-0 space-y-3 rounded-lg border bg-muted/30 p-3 text-sm [overflow-wrap:anywhere]">
      <h3 className="font-medium">{t('connections.form.guide.title')}</h3>
      {!adapterAvailable ? (
        <Alert>
          <AlertDescription>{t('connections.form.guide.unsupportedProvider')}</AlertDescription>
        </Alert>
      ) : !authSupported && (
        <Alert variant="destructive">
          <AlertDescription>{t('connections.form.guide.unsupportedAuth')}</AlertDescription>
        </Alert>
      )}
      <p className="leading-relaxed text-muted-foreground">{t(`connections.form.guide.${authMethod}`)}</p>
      <div className="space-y-1">
        <h4 className="font-medium">{t('connections.form.guide.source')}</h4>
        <p className="leading-relaxed text-muted-foreground">{t(`connections.form.guide.${source}`)}</p>
        {docs && (
          <a href={docs} target="_blank" rel="noopener noreferrer" className="inline-block rounded-sm text-primary underline underline-offset-4 focus-visible:outline-2">
            {t('connections.form.guide.docs')}
          </a>
        )}
      </div>
      <p className="border-t pt-3 leading-relaxed">{t('connections.form.nextStep')}</p>
    </section>
  )
}
