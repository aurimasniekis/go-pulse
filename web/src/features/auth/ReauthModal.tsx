import { Alert, Button, Center, Divider, Group, Loader, Modal, PasswordInput, Stack, Text, TextInput } from '@mantine/core'
import { useQueryClient } from '@tanstack/react-query'
import { useEffect, useRef, useState, type ReactNode } from 'react'
import { fetchMe, meKey, useAuthConfig } from '@/api/auth'
import { admin, unwrap, type Me } from '@/api/client'
import { loginUrl, setCsrfToken, setReauthHandler, type ReauthCode } from '@/api/session'
import { errorMessage } from './errors'
import { oidcStartUrl, rememberStepUpReturn } from './sso'

/**
 * "Confirm it's you" (recent authentication). Registered as the session layer's re-authentication handler: when a
 * request fails with reauth_required or mfa_required, the request waits for this prompt and is retried once after
 * success (the session rotates, so the CSRF token is refreshed). Cancelling fails the original request.
 *
 * - Password and break-glass sessions: password, plus a TOTP or recovery code when a factor is enrolled.
 * - SSO sessions: step-up at the identity provider (a full page load; repeat the action afterwards), or the user's
 *   Pulse TOTP when the provider allows that fallback.
 * - Limited sessions (MFA pending) go to /login to finish signing in.
 */
type Mode =
  | { kind: 'loading' }
  | { kind: 'error'; error: unknown }
  | { kind: 'limited' }
  | { kind: 'password'; me: Me }
  | { kind: 'sso'; me: Me }

const here = () => `${window.location.pathname}${window.location.search}`

export function ReauthModal({ navigate = (url: string) => window.location.assign(url) }: { navigate?: (url: string) => void }) {
  const qc = useQueryClient()
  const config = useAuthConfig()
  const [open, setOpen] = useState(false)
  const [mode, setMode] = useState<Mode>({ kind: 'loading' })
  const [reason, setReason] = useState<ReauthCode>('reauth_required')
  const [ssoTotp, setSsoTotp] = useState(false)
  const [password, setPassword] = useState('')
  const [code, setCode] = useState('')
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const resolver = useRef<((ok: boolean) => void) | null>(null)

  useEffect(
    () =>
      setReauthHandler(
        (cause) =>
          new Promise<boolean>((resolve) => {
            resolver.current = resolve
            setReason(cause)
            setError(null)
            setSsoTotp(false)
            setPassword('')
            setCode('')
            setMode({ kind: 'loading' })
            setOpen(true)
            fetchMe()
              .then((me) => {
                if (!me || me.mfa_pending || me.must_enroll) setMode({ kind: 'limited' })
                else if (me.auth_method === 'oidc') setMode({ kind: 'sso', me })
                else setMode({ kind: 'password', me })
              })
              .catch((e: unknown) => setMode({ kind: 'error', error: e }))
          }),
      ),
    [],
  )

  const finish = (ok: boolean) => {
    resolver.current?.(ok)
    resolver.current = null
    setOpen(false)
  }

  const run = async (call: () => Promise<Me>) => {
    setPending(true)
    setError(null)
    try {
      const me = await call()
      setCsrfToken(me.csrf_token)
      qc.setQueryData(meKey, me)
      finish(true)
    } catch (e) {
      setError(e)
    } finally {
      setPending(false)
    }
  }

  const cancel = () => {
    if (!pending) finish(false)
  }

  const errorAlert = error !== null ? <Alert color="red">{errorMessage(error)}</Alert> : null
  const actions = (submit: string | null) => (
    <Group justify="flex-end">
      <Button variant="default" onClick={cancel} disabled={pending}>
        Cancel
      </Button>
      {submit && (
        <Button type="submit" loading={pending}>
          {submit}
        </Button>
      )}
    </Group>
  )

  let body: ReactNode
  switch (mode.kind) {
    case 'loading':
      body = (
        <Center py="md">
          <Loader size="sm" />
        </Center>
      )
      break
    case 'error':
      body = (
        <Stack gap="sm">
          <Alert color="red">{errorMessage(mode.error)}</Alert>
          {actions(null)}
        </Stack>
      )
      break
    case 'limited':
      body = (
        <Stack gap="sm">
          <Text size="sm">Your sign-in is not complete. Finish two-factor authentication to continue.</Text>
          <Group justify="flex-end">
            <Button variant="default" onClick={cancel}>
              Cancel
            </Button>
            <Button
              onClick={() => {
                finish(false)
                navigate(loginUrl(here()))
              }}
            >
              Go to sign-in
            </Button>
          </Group>
        </Stack>
      )
      break
    case 'password': {
      const needsCode = mode.me.user.mfa_enrolled
      body = (
        <form
          aria-label="Re-authenticate with password"
          onSubmit={(e) => {
            e.preventDefault()
            void run(async () => unwrap(await admin.POST('/auth/reauth', { body: { password, code: needsCode ? code.trim() : undefined } })))
          }}
        >
          <Stack gap="sm">
            <Text size="sm">
              {reason === 'mfa_required'
                ? 'This action needs a recent second-factor check. Enter your password and authenticator code.'
                : 'This action needs a recent sign-in. Enter your password to continue.'}
            </Text>
            <PasswordInput label="Password" autoComplete="current-password" required data-autofocus value={password}
              onChange={(e) => setPassword(e.currentTarget.value)} />
            {needsCode && (
              <TextInput label="Authenticator or recovery code" autoComplete="one-time-code" maxLength={32} required value={code}
                onChange={(e) => setCode(e.currentTarget.value)} />
            )}
            {reason === 'mfa_required' && !needsCode && (
              <Alert color="yellow">This action needs two-factor authentication. Set up an authenticator on your Account page first.</Alert>
            )}
            {errorAlert}
            {actions('Verify')}
          </Stack>
        </form>
      )
      break
    }
    case 'sso': {
      const providers = (config.data?.providers ?? []).filter((p) => !mode.me.auth_provider || p.slug === mode.me.auth_provider)
      body =
        ssoTotp && mode.me.user.mfa_enrolled ? (
          <form
            aria-label="Re-authenticate with authenticator code"
            onSubmit={(e) => {
              e.preventDefault()
              void run(async () => unwrap(await admin.POST('/auth/reauth/totp', { body: { code: code.trim() } })))
            }}
          >
            <Stack gap="sm">
              <Text size="sm">Enter the code from your Pulse authenticator app, or a recovery code.</Text>
              <TextInput label="Authenticator or recovery code" autoComplete="one-time-code" maxLength={32} required data-autofocus value={code}
                onChange={(e) => setCode(e.currentTarget.value)} />
              {errorAlert}
              {actions('Verify')}
            </Stack>
          </form>
        ) : (
          <Stack gap="sm">
            <Text size="sm">
              This action needs a recent sign-in. You will be sent to your identity provider and back here; then repeat the action.
            </Text>
            {providers.length === 0 ? (
              <Alert color="yellow">No single sign-on provider is enabled for step-up.</Alert>
            ) : (
              providers.map((p) => (
                <Button
                  key={p.slug}
                  variant="default"
                  fullWidth
                  onClick={() => {
                    rememberStepUpReturn(here())
                    finish(false)
                    navigate(oidcStartUrl(p.slug, 'stepup', here()))
                  }}
                >
                  Continue with {p.display_name}
                </Button>
              ))
            )}
            {mode.me.user.mfa_enrolled && (
              <>
                <Divider label="or" labelPosition="center" />
                <Button variant="subtle" fullWidth onClick={() => setSsoTotp(true)}>
                  Use my authenticator code instead
                </Button>
              </>
            )}
            {actions(null)}
          </Stack>
        )
      break
    }
  }

  return (
    <Modal opened={open} onClose={cancel} title="Confirm it's you" closeOnClickOutside={false} withCloseButton={!pending} zIndex={400}>
      {body}
    </Modal>
  )
}
