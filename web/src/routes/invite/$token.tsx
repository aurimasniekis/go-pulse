import { createFileRoute } from '@tanstack/react-router'
import { AcceptInvitationPage } from '@/features/auth/TokenPages'

export const Route = createFileRoute('/invite/$token')({ component: Invite })

function Invite() {
  const { token } = Route.useParams()
  return <AcceptInvitationPage token={token} onSignIn={() => window.location.assign('/login')} />
}
