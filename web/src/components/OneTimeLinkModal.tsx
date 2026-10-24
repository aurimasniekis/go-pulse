import { Alert, Button, Code, CopyButton, Group, Modal, Stack, Text } from '@mantine/core'
import { IconAlertTriangle, IconCheck, IconCopy } from '@tabler/icons-react'
import type { OneTimeLink } from '@/api/client'
import { formatTime } from '@/lib/format'

/** Shows a single-use link (invitation, password link, SSO link invitation) once, as a full URL. */
export function OneTimeLinkModal({ link, title, children, onClose }: {
  link: OneTimeLink | null
  title: string
  children?: React.ReactNode
  onClose: () => void
}) {
  const url = link ? window.location.origin + link.url : ''
  return (
    <Modal opened={link !== null} onClose={onClose} title={title} size="lg" closeOnClickOutside={false}>
      <Stack gap="sm">
        <Alert color="yellow" icon={<IconAlertTriangle size={16} />}>
          This link is shown once and works once{link ? `, until ${formatTime(link.expires_at)}` : ''}. Send it over a channel you trust.
        </Alert>
        {children}
        <Code block data-testid="one-time-link" style={{ wordBreak: 'break-all', whiteSpace: 'pre-wrap' }}>
          {url}
        </Code>
        <Group justify="space-between">
          <CopyButton value={url}>
            {({ copied, copy }) => (
              <Button size="xs" onClick={copy} leftSection={copied ? <IconCheck size={14} /> : <IconCopy size={14} />}>
                {copied ? 'Copied' : 'Copy link'}
              </Button>
            )}
          </CopyButton>
          <Button onClick={onClose}>Done</Button>
        </Group>
        <Text size="xs" c="dimmed">
          The origin is the one you are using now. If the person reaches the dashboard elsewhere, give them the path instead.
        </Text>
      </Stack>
    </Modal>
  )
}
