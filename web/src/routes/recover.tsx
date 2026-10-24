import { createFileRoute } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { enterApp, stripTokenFromURL } from '@/features/auth/navigation'
import { RecoverPage } from '@/features/auth/RecoverPage'

export const Route = createFileRoute('/recover')({
  validateSearch: (s: Record<string, unknown>): { token?: string } => ({ token: typeof s.token === 'string' ? s.token : undefined }),
  component: Recover,
})

function Recover() {
  const [token] = useState(Route.useSearch().token ?? '')
  useEffect(stripTokenFromURL, [])
  return <RecoverPage initialToken={token} onDone={() => enterApp('/audit')} onResetPassword={(p) => window.location.assign(p)} />
}
