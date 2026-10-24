import { Alert, Anchor, Button, Center, Code, Divider, Loader, PasswordInput, Stack, Text, TextInput } from '@mantine/core'
import { useQuery } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { fetchMe, useAuthConfig } from '@/api/auth'
import { admin, unwrap, type AuthConfig, type Me } from '@/api/client'
import { setCsrfToken } from '@/api/session'
import { AuthLayout } from './AuthLayout'
import { CodeForm } from './CodeForm'
import { errorMessage } from './errors'
import { oidcStartUrl, ssoErrorMessage, takeStepUpReturn } from './sso'
import { TotpEnrollment } from './TotpEnrollment'

/**
 * Sign-in (ADR-0038). Renders from GET /auth/config: a password form, SSO buttons, or both. POST /auth/login returns
 * {mfa_pending, must_enroll, me}: pending MFA moves to the code step (POST /auth/mfa), a required enrollment to TOTP
 * setup. Enrollment does not finish sign-in: the code step follows with a later code. An existing limited session
 * (after a reload) resumes at the right step. SSO errors arrive as ?sso_error=<code>; for mfa_required after an SSO
 * step-up the TOTP fallback (POST /auth/reauth/totp) is offered.
 */

type Step = 'credentials' | 'mfa' | 'enroll' | 'stepup-totp'

export const AFTER_ENROLL_INTRO =
  'Your authenticator is set up. To finish signing in, wait for it to show a new code and enter that code (or one of your recovery codes).'

/** Where to resume for an existing session (null: show the credentials form). */
export function resumeStep(me: Me | null | undefined, ssoError: string | undefined): Step | 'done' | null {
  if (!me) return null
  if (ssoError === 'mfa_required' && me.auth_method === 'oidc' && !me.mfa_pending && !me.must_enroll) {
    return me.user.mfa_enrolled ? 'stepup-totp' : null
  }
  if (me.must_enroll) return 'enroll'
  if (me.mfa_pending) return 'mfa'
  return ssoError ? null : 'done'
}

async function logoutQuietly() {
  await admin.POST('/auth/logout').catch(() => undefined)
  setCsrfToken('')
}

function LocalLoginForm({ onResult }: { onResult: (step: Step | 'done') => void }) {
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<unknown>(null)
  return (
    <form
      aria-label="Sign in with email and password"
      onSubmit={(e) => {
        e.preventDefault()
        setPending(true)
        setError(null)
        admin
          .POST('/auth/login', { body: { email: email.trim(), password } })
          .then((r) => {
            const res = unwrap(r)
            setCsrfToken(res.me.csrf_token)
            onResult(res.must_enroll ? 'enroll' : res.mfa_pending ? 'mfa' : 'done')
          })
          .catch(setError)
          .finally(() => setPending(false))
      }}
    >
      <Stack gap="sm">
        <TextInput label="Email" type="email" autoComplete="username" autoCapitalize="none" spellCheck={false} required
          value={email} onChange={(e) => setEmail(e.currentTarget.value)} />
        <PasswordInput label="Password" autoComplete="current-password" required value={password}
          onChange={(e) => setPassword(e.currentTarget.value)} />
        {error !== null && <Alert color="red">{errorMessage(error)}</Alert>}
        <Button type="submit" loading={pending} fullWidth>
          Sign in
        </Button>
      </Stack>
    </form>
  )
}

export function LoginForms({
  config,
  next,
  onSignedIn,
  initialStep = 'credentials',
  ssoError,
  linked,
}: {
  config: AuthConfig
  next: string
  onSignedIn: (target?: string) => void
  initialStep?: Step
  ssoError?: string
  linked?: boolean
}) {
  const [step, setStep] = useState<Step>(initialStep)
  const [justEnrolled, setJustEnrolled] = useState(false)
  const back = () => {
    void logoutQuietly()
    setStep('credentials')
  }

  if (step === 'mfa') {
    return (
      <CodeForm
        label="Two-factor authentication"
        intro={justEnrolled ? AFTER_ENROLL_INTRO : 'Enter the code from your authenticator app, or one of your recovery codes.'}
        onSubmit={async (code) => {
          const me = unwrap(await admin.POST('/auth/mfa', { body: { code } }))
          setCsrfToken(me.csrf_token)
          onSignedIn()
        }}
        onBack={back}
      />
    )
  }
  if (step === 'enroll') {
    return (
      <Stack gap="sm">
        <Alert color="blue" title="Set up two-factor authentication">
          Your account requires an authenticator app before you can continue.
        </Alert>
        <TotpEnrollment
          onDone={() => {
            setJustEnrolled(true)
            setStep('mfa')
          }}
          onCancel={back}
        />
      </Stack>
    )
  }
  if (step === 'stepup-totp') {
    return (
      <CodeForm
        label="Confirm with authenticator code"
        intro={ssoErrorMessage('mfa_required')}
        onSubmit={async (code) => {
          unwrap(await admin.POST('/auth/reauth/totp', { body: { code } }))
          onSignedIn(takeStepUpReturn())
        }}
        onBack={() => onSignedIn(takeStepUpReturn())}
        backLabel="Skip and return"
      />
    )
  }

  const providers = config.providers
  const error = ssoError ? <Alert color="red">{ssoErrorMessage(ssoError)}</Alert> : null
  if (!config.local_login && providers.length === 0) {
    return (
      <Stack gap="sm">
        {error}
        <Alert color="yellow" title="No sign-in method is enabled">
          An administrator can restore access with <Code>pulsed admin recover</Code> on the server.
        </Alert>
      </Stack>
    )
  }
  return (
    <Stack gap="sm">
      {error}
      {linked && <Alert color="teal">Your single sign-on identity is linked. Sign in to continue.</Alert>}
      {config.bootstrap_required && (
        <Alert color="blue" title="Pulse is not set up yet">
          Run <Code>pulsed admin bootstrap</Code> on the server and open the setup link it prints.
        </Alert>
      )}
      {config.local_login ? (
        <LocalLoginForm onResult={(s) => (s === 'done' ? onSignedIn() : setStep(s))} />
      ) : (
        <Text size="sm" c="dimmed">
          Password sign-in is disabled. Use single sign-on.
        </Text>
      )}
      {config.local_login && providers.length > 0 && <Divider label="or" labelPosition="center" />}
      {providers.length > 0 &&
        (config.sso_here ? (
          <Stack gap="xs" aria-label="Single sign-on providers">
            {providers.map((p) => (
              <Button key={p.slug} variant="default" component="a" href={oidcStartUrl(p.slug, 'login', next)} fullWidth>
                Sign in with {p.display_name}
              </Button>
            ))}
          </Stack>
        ) : (
          config.sso_url && (
            <Text size="sm" c="dimmed">
              Single sign-on is available at{' '}
              <Anchor href={`${config.sso_url}/login`}>{config.sso_url.replace(/^https?:\/\//, '')}</Anchor>.
            </Text>
          )
        ))}
    </Stack>
  )
}

export function LoginPage({
  next,
  ssoError,
  linked,
  onSignedIn,
}: {
  next: string
  ssoError?: string
  linked?: boolean
  onSignedIn: (target?: string) => void
}) {
  const config = useAuthConfig()
  // An existing session (limited, or a full one after an SSO step-up fallback) decides the first step.
  const session = useQuery({ queryKey: ['login-session'], queryFn: fetchMe, retry: false, staleTime: 0, gcTime: 0 })
  const resume = session.isSuccess ? resumeStep(session.data, ssoError) : null

  useEffect(() => {
    if (resume === 'done') onSignedIn()
  }, [resume, onSignedIn])

  return (
    <AuthLayout title="Sign in to Pulse">
      {config.isPending || session.isPending || resume === 'done' ? (
        <Center py="md">
          <Loader size="sm" />
        </Center>
      ) : config.isError ? (
        <Alert color="red" title="Sign-in options unavailable">
          {errorMessage(config.error)}
        </Alert>
      ) : (
        <LoginForms
          config={config.data}
          next={next}
          onSignedIn={onSignedIn}
          ssoError={ssoError}
          linked={linked}
          {...(resume ? { initialStep: resume } : {})}
        />
      )}
    </AuthLayout>
  )
}
