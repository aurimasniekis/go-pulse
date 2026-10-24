import { Alert, Button, Code, PasswordInput, Stack } from '@mantine/core'
import { useState } from 'react'
import { admin, unwrap } from '@/api/client'
import { setCsrfToken } from '@/api/session'
import { AuthLayout } from './AuthLayout'
import { errorMessage } from './errors'
import { TotpEnrollment } from './TotpEnrollment'

/**
 * Break-glass recovery: `pulsed admin recover` prints a single-use /recover?token=… link (30 minutes); the route strips
 * the token from the address bar. Nothing is redeemed on page load (link scanners must not burn the token). Redeeming
 * signs out the account's other sessions and removes its TOTP factor; accounts that must have MFA enroll a new one, and
 * accounts with a password get a one-time link to set a password they know.
 */
export function RecoverPage({ initialToken, onDone, onResetPassword }: {
  initialToken: string
  onDone: () => void
  onResetPassword: (path: string) => void
}) {
  const [step, setStep] = useState<'token' | 'enroll' | 'done'>('token')
  const [token, setToken] = useState(initialToken)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [resetPath, setResetPath] = useState<string | null>(null)

  const redeem = async () => {
    setPending(true)
    setError(null)
    try {
      const res = unwrap(await admin.POST('/auth/recover', { body: { token: token.trim() } }))
      setCsrfToken(res.me.csrf_token)
      setResetPath(res.password_reset ? `/reset/${encodeURIComponent(res.password_reset.token)}` : null)
      setStep(res.must_enroll ? 'enroll' : 'done')
    } catch (e) {
      setError(e)
    } finally {
      setPending(false)
    }
  }

  return (
    <AuthLayout title="Break-glass recovery" subtitle="Single-use recovery login issued on the server.">
      {step === 'token' ? (
        <form aria-label="Redeem recovery login" onSubmit={(e) => { e.preventDefault(); void redeem() }}>
          <Stack gap="sm">
            <Alert color="yellow" title="This signs you in with a break-glass session">
              Other sessions of this account end, and its authenticator and recovery codes are removed. Issuing and using the
              login are written to the audit log.
            </Alert>
            <PasswordInput label="Recovery token" autoComplete="off" required value={token} onChange={(e) => setToken(e.currentTarget.value)}
              description={<>Printed by <Code>pulsed admin recover</Code>. Valid once, for 30 minutes.</>} />
            {error !== null && <Alert color="red">{errorMessage(error)}</Alert>}
            <Button type="submit" loading={pending} fullWidth>
              Use recovery login
            </Button>
          </Stack>
        </form>
      ) : step === 'enroll' ? (
        <Stack gap="sm">
          <Alert color="blue" title="Set up a new authenticator">
            This account requires two-factor authentication. Enroll a new authenticator to continue.
          </Alert>
          <TotpEnrollment onDone={() => setStep('done')} />
        </Stack>
      ) : (
        <Stack gap="sm">
          <Alert color="teal" title="Signed in for recovery">
            Review the audit log and rotate credentials as needed.
          </Alert>
          {resetPath && (
            <Alert color="yellow" title="Set a new password">
              <Stack gap="xs" align="flex-start">
                Use the one-time link (valid for 1 hour) to set a password you know. It signs you out of every session,
                including this one, so finish urgent recovery work first.
                <Button size="xs" onClick={() => onResetPassword(resetPath)}>
                  Set password
                </Button>
              </Stack>
            </Alert>
          )}
          <Button onClick={onDone}>Continue to Pulse</Button>
        </Stack>
      )}
    </AuthLayout>
  )
}
