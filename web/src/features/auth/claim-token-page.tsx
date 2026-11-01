import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { Link, Navigate, useNavigate } from 'react-router'
import { AlertCircleIcon } from 'lucide-react'
import { useAuthStatus, useClaimToken } from '@/api/hooks/auth'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import { Ltr } from '@/components/ltr'
import { takeHashToken } from '@/lib/hash-token'
import { problemMessage } from '@/lib/problem-message'
import { AuthLayout } from './auth-layout'

export function ClaimTokenPage({ kind }: { kind: 'bootstrap' | 'redeem' }) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const status = useAuthStatus()
  const claim = useClaimToken(kind)
  const [token, setToken] = useState(takeHashToken)
  const ns = kind === 'bootstrap' ? 'auth.bootstrap' : 'auth.login'

  if (status.isPending) {
    return (
      <AuthLayout>
        <Skeleton className="h-64 w-full" />
      </AuthLayout>
    )
  }
  if (status.isError) {
    return (
      <AuthLayout>
        <Alert variant="destructive">
          <AlertCircleIcon />
          <AlertTitle>{problemMessage(t, status.error)}</AlertTitle>
          <AlertDescription>
            <Button variant="outline" size="sm" onClick={() => void status.refetch()}>
              {t('common.retry')}
            </Button>
          </AlertDescription>
        </Alert>
      </AuthLayout>
    )
  }

  const { bootstrapped, authenticated } = status.data
  if (kind === 'redeem' && !bootstrapped) return <Navigate to="/bootstrap" replace />
  if (kind === 'redeem' && authenticated && !claim.isSuccess) return <Navigate to="/" replace />

  if (kind === 'bootstrap' && bootstrapped && !claim.isSuccess) {
    return (
      <AuthLayout>
        <Card>
          <CardHeader>
            <CardTitle>{t('auth.bootstrap.alreadyTitle')}</CardTitle>
            <CardDescription>{t('auth.bootstrap.alreadyBody')}</CardDescription>
          </CardHeader>
          <CardFooter>
            <Button asChild>
              <Link to={authenticated ? '/' : '/login'}>
                {authenticated ? t('auth.bootstrap.toApp') : t('auth.bootstrap.toLogin')}
              </Link>
            </Button>
          </CardFooter>
        </Card>
      </AuthLayout>
    )
  }

  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (!token.trim()) return
    claim.mutate(token.trim(), { onSuccess: () => navigate('/', { replace: true }) })
  }

  return (
    <AuthLayout>
      <Card>
        <form onSubmit={submit}>
          <CardHeader>
            <CardTitle>{t(`${ns}.title`)}</CardTitle>
            <CardDescription>{t(`${ns}.body`)}</CardDescription>
          </CardHeader>
          <CardContent className="mt-4 flex flex-col gap-4">
            {kind === 'redeem' && (
              <p className="rounded-md bg-muted p-3 text-sm">
                <Ltr>{t('auth.login.command')}</Ltr>
              </p>
            )}
            <div className="flex flex-col gap-2">
              <Label htmlFor="token">{t(`${ns}.token`)}</Label>
              <Input
                id="token"
                dir="ltr"
                type="password"
                autoComplete="off"
                spellCheck={false}
                className="font-mono"
                value={token}
                onChange={(e) => setToken(e.target.value)}
                aria-describedby="token-hint"
              />
              <p id="token-hint" className="text-xs text-muted-foreground">
                {t(`${ns}.tokenHint`)}
              </p>
            </div>
            {claim.isError && (
              <Alert variant="destructive" role="alert">
                <AlertCircleIcon />
                <AlertDescription>{problemMessage(t, claim.error)}</AlertDescription>
              </Alert>
            )}
          </CardContent>
          <CardFooter className="mt-4">
            <Button type="submit" disabled={claim.isPending || !token.trim()}>
              {kind === 'bootstrap'
                ? claim.isPending
                  ? t('auth.bootstrap.claiming')
                  : t('auth.bootstrap.claim')
                : claim.isPending
                  ? t('auth.login.submitting')
                  : t('auth.login.submit')}
            </Button>
          </CardFooter>
        </form>
      </Card>
    </AuthLayout>
  )
}

export const BootstrapPage = () => <ClaimTokenPage kind="bootstrap" />
export const LoginPage = () => <ClaimTokenPage kind="redeem" />
