import { PasswordInput, Stack } from '@mantine/core'

/** New password + confirmation (controlled). Validate with newPasswordError before submitting. */
export function NewPasswordFields({
  label = 'Password',
  password,
  confirm,
  onChange,
}: {
  label?: string
  password: string
  confirm: string
  onChange: (password: string, confirm: string) => void
}) {
  return (
    <Stack gap="sm">
      <PasswordInput label={label} description="At least 12 characters." autoComplete="new-password" required value={password}
        onChange={(e) => onChange(e.currentTarget.value, confirm)} />
      <PasswordInput label="Confirm password" autoComplete="new-password" required value={confirm}
        onChange={(e) => onChange(password, e.currentTarget.value)} />
    </Stack>
  )
}
