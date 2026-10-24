import { createFileRoute } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { enterApp, stripTokenFromURL } from '@/features/auth/navigation'
import { SetupPage } from '@/features/auth/SetupPage'

export const Route = createFileRoute('/setup')({
  validateSearch: (s: Record<string, unknown>): { token?: string } => ({ token: typeof s.token === 'string' ? s.token : undefined }),
  component: Setup,
})

function Setup() {
  const [token] = useState(Route.useSearch().token ?? '')
  useEffect(stripTokenFromURL, [])
  return <SetupPage initialToken={token} onDone={() => enterApp('/')} />
}
