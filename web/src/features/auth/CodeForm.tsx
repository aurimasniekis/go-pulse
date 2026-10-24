import { Alert, Button, Stack, Text, TextInput } from '@mantine/core'
import { useState } from 'react'
import { errorMessage } from './errors'

/** One code field (TOTP or recovery code) with a submit button; used by sign-in, recovery and step-up. */
export function CodeForm({
  label,
  intro,
  onSubmit,
  onBack,
  backLabel = 'Use a different account',
}: {
  label: string
  intro: string
  onSubmit: (code: string) => Promise<void>
  onBack?: () => void
  backLabel?: string
}) {
  const [code, setCode] = useState('')
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<unknown>(null)
  return (
    <form
      aria-label={label}
      onSubmit={(e) => {
        e.preventDefault()
        setPending(true)
        setError(null)
        onSubmit(code.trim())
          .catch(setError)
          .finally(() => setPending(false))
      }}
    >
      <Stack gap="sm">
        <Text size="sm">{intro}</Text>
        <TextInput
          label="Authentication code"
          autoComplete="one-time-code"
          spellCheck={false}
          maxLength={32}
          required
          data-autofocus
          autoFocus
          value={code}
          onChange={(e) => setCode(e.currentTarget.value)}
        />
        {error !== null && <Alert color="red">{errorMessage(error)}</Alert>}
        <Button type="submit" loading={pending} fullWidth>
          Verify
        </Button>
        {onBack && (
          <Button variant="subtle" onClick={onBack} disabled={pending} fullWidth>
            {backLabel}
          </Button>
        )}
      </Stack>
    </form>
  )
}
