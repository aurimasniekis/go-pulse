import { Alert, Button, Code, CopyButton, Group, Modal, ScrollArea, Stack } from '@mantine/core'
import { IconAlertTriangle, IconCheck, IconCopy, IconDownload } from '@tabler/icons-react'
import type { ReactNode } from 'react'

interface Props {
  secret: string | null
  title: string
  filename?: string
  onClose: () => void
  children?: ReactNode
}

/**
 * Shows a secret exactly once (PL-UI-008): copy and download, never stored in the query cache. The caller drops the
 * value from state on close.
 */
export function SecretModal({ secret, title, filename, onClose, children }: Props) {
  const download = () => {
    if (!secret || !filename) return
    const url = URL.createObjectURL(new Blob([secret], { type: 'text/plain' }))
    const a = document.createElement('a')
    a.href = url
    a.download = filename
    a.click()
    URL.revokeObjectURL(url)
  }
  return (
    <Modal opened={secret !== null} onClose={onClose} title={title} size="xl" closeOnClickOutside={false}>
      <Stack gap="sm">
        <Alert color="yellow" icon={<IconAlertTriangle size={16} />}>
          This is the only time the secret is shown. Pulse keeps only what it needs to verify it.
        </Alert>
        <ScrollArea.Autosize mah={260}>
          <Code block data-testid="secret-value" style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>
            {secret}
          </Code>
        </ScrollArea.Autosize>
        <Group>
          <CopyButton value={secret ?? ''}>
            {({ copied, copy }) => (
              <Button size="xs" leftSection={copied ? <IconCheck size={14} /> : <IconCopy size={14} />} onClick={copy}>
                {copied ? 'Copied' : 'Copy'}
              </Button>
            )}
          </CopyButton>
          {filename && (
            <Button size="xs" variant="default" leftSection={<IconDownload size={14} />} onClick={download}>
              Download {filename}
            </Button>
          )}
        </Group>
        {children}
        <Group justify="flex-end">
          <Button onClick={onClose}>I saved it</Button>
        </Group>
      </Stack>
    </Modal>
  )
}

/** Downloads text as a file; shared with snippets. */
export function saveText(name: string, text: string) {
  const url = URL.createObjectURL(new Blob([text], { type: 'text/plain' }))
  const a = document.createElement('a')
  a.href = url
  a.download = name
  a.click()
  URL.revokeObjectURL(url)
}
