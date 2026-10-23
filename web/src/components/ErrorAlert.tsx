import { Alert } from '@mantine/core'
import { IconAlertCircle } from '@tabler/icons-react'

export function ErrorAlert({ error, title = 'Request failed' }: { error: unknown; title?: string }) {
  if (!error) return null
  const msg = error instanceof Error ? error.message : String(error)
  return (
    <Alert color="red" icon={<IconAlertCircle size={16} />} title={title} mb="sm">
      {msg}
    </Alert>
  )
}
