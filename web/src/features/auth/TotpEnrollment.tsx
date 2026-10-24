import { Alert, Button, Center, Code, CopyButton, Group, Loader, Stack, Text, TextInput } from '@mantine/core'
import { IconCheck, IconCopy } from '@tabler/icons-react'
import { useEffect, useRef, useState } from 'react'
import { admin, unwrap, type Schemas } from '@/api/client'
import { QRCode } from '@/components/QRCode'
import { SecretModal } from '@/components/SecretModal'
import { errorMessage } from './errors'

/**
 * TOTP enrollment: POST /account/totp/begin returns a secret and otpauth:// URI (QR rendered locally), the user
 * confirms a code with POST /account/totp/confirm, which returns recovery codes shown exactly once.
 *
 * A limited session (sign-in, setup, break-glass) enrolls without a re-authentication prompt; a password session then
 * still finishes sign-in with a later code. A full session replacing its factor needs recent authentication (the
 * re-authentication prompt handles it) and is rotated on confirmation.
 */
export function TotpEnrollment({ onDone, onCancel }: { onDone: () => void; onCancel?: () => void }) {
  const [enrollment, setEnrollment] = useState<Schemas['TOTPEnrollment'] | null>(null)
  const [codes, setCodes] = useState<string[] | null>(null)
  const [code, setCode] = useState('')
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const started = useRef(false)

  const begin = async () => {
    setPending(true)
    setError(null)
    try {
      setEnrollment(unwrap(await admin.POST('/account/totp/begin')))
    } catch (e) {
      setError(e)
    } finally {
      setPending(false)
    }
  }

  useEffect(() => {
    // Start once (StrictMode mounts twice in development).
    if (!started.current) {
      started.current = true
      void begin()
    }
  }, [])

  const confirm = async () => {
    setPending(true)
    setError(null)
    try {
      const res = unwrap(await admin.POST('/account/totp/confirm', { body: { code: code.trim() } }))
      setCodes(res.recovery_codes)
    } catch (e) {
      setError(e)
    } finally {
      setPending(false)
    }
  }

  return (
    <Stack gap="sm">
      {enrollment === null ? (
        error !== null ? (
          <Alert color="red" title="Could not start enrollment">
            <Stack gap="xs">
              <Text size="sm">{errorMessage(error)}</Text>
              <Group>
                <Button size="xs" onClick={() => void begin()}>
                  Retry
                </Button>
                {onCancel && (
                  <Button size="xs" variant="default" onClick={onCancel}>
                    Cancel
                  </Button>
                )}
              </Group>
            </Stack>
          </Alert>
        ) : (
          <Center py="md">
            <Loader size="sm" />
          </Center>
        )
      ) : (
        <form
          aria-label="Confirm authenticator"
          onSubmit={(e) => {
            e.preventDefault()
            void confirm()
          }}
        >
          <Stack gap="sm">
            <Text size="sm">Scan this code with an authenticator app (or enter the secret), then enter the 6-digit code it shows.</Text>
            <Center>
              <QRCode value={enrollment.otpauth_uri} label="Authenticator QR code" />
            </Center>
            <Group gap="xs" wrap="nowrap">
              <Code style={{ flex: 1, wordBreak: 'break-all' }} data-testid="totp-secret">
                {enrollment.secret}
              </Code>
              <CopyButton value={enrollment.secret}>
                {({ copied, copy }) => (
                  <Button size="xs" variant="default" onClick={copy} leftSection={copied ? <IconCheck size={14} /> : <IconCopy size={14} />}>
                    {copied ? 'Copied' : 'Copy'}
                  </Button>
                )}
              </CopyButton>
            </Group>
            <TextInput
              label="Code from your app"
              inputMode="numeric"
              autoComplete="one-time-code"
              maxLength={8}
              required
              value={code}
              onChange={(e) => setCode(e.currentTarget.value)}
            />
            {error !== null && <Alert color="red">{errorMessage(error)}</Alert>}
            <Group justify="flex-end">
              {onCancel && (
                <Button variant="default" onClick={onCancel} disabled={pending}>
                  Cancel
                </Button>
              )}
              <Button type="submit" loading={pending}>
                Confirm
              </Button>
            </Group>
          </Stack>
        </form>
      )}
      <SecretModal
        secret={codes ? codes.join('\n') : null}
        title="Recovery codes"
        filename="pulse-recovery-codes.txt"
        onClose={() => {
          setCodes(null)
          onDone()
        }}
      >
        <Text size="sm" c="dimmed">
          Each code signs you in once if you lose your authenticator. Store them offline.
        </Text>
      </SecretModal>
    </Stack>
  )
}
