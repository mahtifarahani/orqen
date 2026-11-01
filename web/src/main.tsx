import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryCache, QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { RouterProvider } from 'react-router'
import './index.css'
import './i18n'
import { ApiProblem } from '@/api/problem'
import { authStatusKey } from '@/api/hooks/auth'
import { LocaleDirection } from '@/components/locale-direction'
import { ThemeProvider } from '@/components/theme-provider'
import { Toaster } from '@/components/ui/sonner'
import { applyLocale, storedLocale } from '@/i18n/direction'
import { router } from './router'

const queryClient: QueryClient = new QueryClient({
  queryCache: new QueryCache({
    onError: (error, query) => {
      // A session that expired mid-use sends the user back through RequireSession.
      if (error instanceof ApiProblem && error.status === 401 && query.queryKey[0] !== '/api/v1/auth/status') {
        void queryClient.invalidateQueries({ queryKey: authStatusKey })
      }
    },
  }),
  defaultOptions: {
    queries: {
      staleTime: 5_000,
      refetchOnWindowFocus: false,
      retry: (count, error) => !(error instanceof ApiProblem && error.status < 500) && count < 2,
    },
  },
})

applyLocale(storedLocale() ?? 'en')

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <ThemeProvider>
      <QueryClientProvider client={queryClient}>
        <LocaleDirection>
          <RouterProvider router={router} />
        </LocaleDirection>
        <Toaster dir="auto" />
      </QueryClientProvider>
    </ThemeProvider>
  </StrictMode>,
)
