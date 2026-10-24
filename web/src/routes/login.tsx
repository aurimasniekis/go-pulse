import { createFileRoute } from '@tanstack/react-router'
import { useCallback } from 'react'
import { LoginPage } from '@/features/auth/LoginPage'
import { enterApp } from '@/features/auth/navigation'

interface LoginSearch {
  return_to?: string
  sso_error?: string
  linked?: string
}

export const Route = createFileRoute('/login')({
  validateSearch: (s: Record<string, unknown>): LoginSearch => ({
    return_to: typeof s.return_to === 'string' ? s.return_to : undefined,
    sso_error: typeof s.sso_error === 'string' ? s.sso_error : undefined,
    linked: s.linked !== undefined ? String(s.linked) : undefined,
  }),
  component: Login,
})

function Login() {
  const { return_to, sso_error, linked } = Route.useSearch()
  const done = useCallback((target?: string) => enterApp(target ?? return_to), [return_to])
  return <LoginPage next={return_to ?? '/'} ssoError={sso_error} linked={!!linked} onSignedIn={done} />
}
