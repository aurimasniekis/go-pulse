import { createFileRoute } from '@tanstack/react-router'
import { ResetPasswordPage } from '@/features/auth/TokenPages'

export const Route = createFileRoute('/reset/$token')({ component: Reset })

function Reset() {
  const { token } = Route.useParams()
  return <ResetPasswordPage token={token} onSignIn={() => window.location.assign('/login')} />
}
