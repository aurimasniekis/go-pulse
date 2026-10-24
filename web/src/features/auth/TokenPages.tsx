import { Alert, Button, Stack, Text, TextInput } from '@mantine/core'
import { useState } from 'react'
import { admin, check, unwrap } from '@/api/client'
import { AuthLayout } from './AuthLayout'
import { errorMessage } from './errors'
import { NewPasswordFields } from './NewPasswordFields'
import { newPasswordError } from './password'

function Done({ title, text, onSignIn }: { title: string; text: string; onSignIn: () => void }) {
  return (
    <Stack gap="sm">
      <Alert color="teal" title={title}>
        {text}
      </Alert>
      <Button onClick={onSignIn}>Sign in</Button>
    </Stack>
  )
}

/** Accept an invitation (/invite/<token>): choose a display name and password. */
export function AcceptInvitationPage({ token, onSignIn }: { token: string; onSignIn: () => void }) {
  const [name, setName] = useState('')
  const [pw, setPw] = useState({ password: '', confirm: '' })
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [done, setDone] = useState(false)
  const submit = async () => {
    const pwErr = newPasswordError(pw.password, pw.confirm)
    if (pwErr) return setError(new Error(pwErr))
    setPending(true)
    setError(null)
    try {
      unwrap(await admin.POST('/auth/invitations/{token}/accept', {
        params: { path: { token } },
        body: { display_name: name.trim() || undefined, password: pw.password },
      }))
      setDone(true)
    } catch (e) {
      setError(e)
    } finally {
      setPending(false)
    }
  }
  return (
    <AuthLayout title="Join Pulse" subtitle="Finish setting up your account.">
      {token === '' ? (
        <Alert color="red">This invitation link is incomplete.</Alert>
      ) : done ? (
        <Done title="Account created" text="Sign in with your email and new password. You may be asked to set up an authenticator."
          onSignIn={onSignIn} />
      ) : (
        <form aria-label="Accept invitation" onSubmit={(e) => { e.preventDefault(); void submit() }}>
          <Stack gap="sm">
            <TextInput label="Display name" autoComplete="name" value={name} onChange={(e) => setName(e.currentTarget.value)} />
            <NewPasswordFields password={pw.password} confirm={pw.confirm} onChange={(password, confirm) => setPw({ password, confirm })} />
            {error !== null && <Alert color="red">{errorMessage(error)}</Alert>}
            <Button type="submit" loading={pending} fullWidth>
              Create account
            </Button>
          </Stack>
        </form>
      )}
    </AuthLayout>
  )
}

/** Set a password with an administrator-issued link (/reset/<token>, single use, 1 h). */
export function ResetPasswordPage({ token, onSignIn }: { token: string; onSignIn: () => void }) {
  const [pw, setPw] = useState({ password: '', confirm: '' })
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [done, setDone] = useState(false)
  const submit = async () => {
    const pwErr = newPasswordError(pw.password, pw.confirm)
    if (pwErr) return setError(new Error(pwErr))
    setPending(true)
    setError(null)
    try {
      check(await admin.POST('/auth/reset/{token}', { params: { path: { token } }, body: { password: pw.password } }))
      setDone(true)
    } catch (e) {
      setError(e)
    } finally {
      setPending(false)
    }
  }
  return (
    <AuthLayout title="Set password">
      {token === '' ? (
        <Alert color="red">This link is incomplete.</Alert>
      ) : done ? (
        <Done title="Password set" text="Every session of this account was signed out. Sign in with the new password." onSignIn={onSignIn} />
      ) : (
        <form aria-label="Choose a new password" onSubmit={(e) => { e.preventDefault(); void submit() }}>
          <Stack gap="sm">
            <Text size="sm" c="dimmed">
              The link works once and expires an hour after it was issued.
            </Text>
            <NewPasswordFields label="New password" password={pw.password} confirm={pw.confirm}
              onChange={(password, confirm) => setPw({ password, confirm })} />
            {error !== null && <Alert color="red">{errorMessage(error)}</Alert>}
            <Button type="submit" loading={pending} fullWidth>
              Set password
            </Button>
          </Stack>
        </form>
      )}
    </AuthLayout>
  )
}
