import { Alert, Button, Code, PasswordInput, Stack, Stepper, TextInput } from '@mantine/core'
import { useState } from 'react'
import { admin, unwrap } from '@/api/client'
import { setCsrfToken } from '@/api/session'
import { AuthLayout } from './AuthLayout'
import { CodeForm } from './CodeForm'
import { errorMessage } from './errors'
import { AFTER_ENROLL_INTRO } from './LoginPage'
import { NewPasswordFields } from './NewPasswordFields'
import { newPasswordError } from './password'
import { TotpEnrollment } from './TotpEnrollment'

/**
 * First-run setup: `pulsed admin bootstrap` prints a one-time /setup?token=… link (the route strips the token from the
 * address bar). POST /auth/setup creates the first administrator (MFA required), the page signs in with the same
 * credentials (held in memory for this step only), runs TOTP enrollment, then verifies a later code (POST /auth/mfa).
 */
export function SetupPage({ initialToken, onDone }: { initialToken: string; onDone: () => void }) {
  const [step, setStep] = useState(0)
  const [token, setToken] = useState(initialToken)
  const [email, setEmail] = useState('')
  const [name, setName] = useState('')
  const [pw, setPw] = useState({ password: '', confirm: '' })
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<unknown>(null)

  const submit = async () => {
    const pwErr = newPasswordError(pw.password, pw.confirm)
    if (pwErr) {
      setError(new Error(pwErr))
      return
    }
    setPending(true)
    setError(null)
    try {
      unwrap(await admin.POST('/auth/setup', { body: { token: token.trim(), email: email.trim(), display_name: name.trim(), password: pw.password } }))
      const login = unwrap(await admin.POST('/auth/login', { body: { email: email.trim(), password: pw.password } }))
      setCsrfToken(login.me.csrf_token)
      if (login.must_enroll) setStep(1)
      else onDone()
    } catch (e) {
      setError(e)
    } finally {
      setPending(false)
    }
  }

  return (
    <AuthLayout title="Set up Pulse" subtitle="Create the first administrator account.">
      <Stepper active={step} size="xs" mb="xs">
        <Stepper.Step label="Account" />
        <Stepper.Step label="Two-factor" />
        <Stepper.Step label="Verify" />
      </Stepper>
      {step === 0 ? (
        <form
          aria-label="Create administrator"
          onSubmit={(e) => {
            e.preventDefault()
            void submit()
          }}
        >
          <Stack gap="sm">
            <PasswordInput label="Setup token" autoComplete="off" required value={token} onChange={(e) => setToken(e.currentTarget.value)}
              description={<>Printed by <Code>pulsed admin bootstrap</Code>. Valid once, for 30 minutes.</>} />
            <TextInput label="Email" type="email" autoComplete="username" autoCapitalize="none" required value={email}
              onChange={(e) => setEmail(e.currentTarget.value)} />
            <TextInput label="Display name" autoComplete="name" value={name} onChange={(e) => setName(e.currentTarget.value)} />
            <NewPasswordFields password={pw.password} confirm={pw.confirm} onChange={(password, confirm) => setPw({ password, confirm })} />
            {error !== null && <Alert color="red">{errorMessage(error)}</Alert>}
            <Button type="submit" loading={pending} fullWidth>
              Create administrator
            </Button>
          </Stack>
        </form>
      ) : step === 1 ? (
        <TotpEnrollment onDone={() => setStep(2)} />
      ) : (
        <CodeForm
          label="Verify authenticator"
          intro={AFTER_ENROLL_INTRO}
          onSubmit={async (code) => {
            const me = unwrap(await admin.POST('/auth/mfa', { body: { code } }))
            setCsrfToken(me.csrf_token)
            onDone()
          }}
        />
      )}
    </AuthLayout>
  )
}
