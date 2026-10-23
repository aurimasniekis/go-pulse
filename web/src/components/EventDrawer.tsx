import { ActionIcon, Badge, CopyButton, Divider, Drawer, Group, Stack, Table, Text, Tooltip } from '@mantine/core'
import { IconCheck, IconCopy } from '@tabler/icons-react'
import type { EventDTO } from '@/api/client'
import { formatTime, shellQuote } from '@/lib/format'
import { PayloadView } from './PayloadView'

function Copy({ value, label }: { value: string; label: string }) {
  return (
    <CopyButton value={value} timeout={1500}>
      {({ copied, copy }) => (
        <Tooltip label={copied ? 'Copied' : label}>
          <ActionIcon onClick={copy} aria-label={label} color={copied ? 'teal' : 'gray'}>
            {copied ? <IconCheck size={14} /> : <IconCopy size={14} />}
          </ActionIcon>
        </Tooltip>
      )}
    </CopyButton>
  )
}

function Row({ k, v, mono = true, copy }: { k: string; v?: string | number | null; mono?: boolean; copy?: boolean }) {
  return (
    <Table.Tr>
      <Table.Td w={130}>
        <Text size="xs" c="dimmed">
          {k}
        </Text>
      </Table.Td>
      <Table.Td>
        <Group gap={4} wrap="nowrap">
          <Text size="sm" ff={mono ? 'monospace' : undefined} style={{ wordBreak: 'break-all' }}>
            {v === undefined || v === null || v === '' ? '—' : v}
          </Text>
          {copy && v !== undefined && v !== null && v !== '' && <Copy value={String(v)} label={`Copy ${k}`} />}
        </Group>
      </Table.Td>
    </Table.Tr>
  )
}

/** Detail view of one event with a copyable replay command (PL-EXP-008). */
export function EventDrawer({ event, onClose }: { event: EventDTO | null; onClose: () => void }) {
  const replay = event ? `pulse sub ${shellQuote(event.subject)} --seq ${event.seq}` : ''
  const extra = event ? Object.entries(event.headers).filter(([k]) => !k.startsWith('ce-') && k !== 'Nats-Msg-Id' && k !== 'content-type') : []
  return (
    <Drawer opened={event !== null} onClose={onClose} position="right" size="xl" title={event ? `${event.stream} #${event.seq}` : ''} data-testid="event-drawer">
      {event && (
        <Stack gap="md">
          <Group gap={6}>
            {event.untyped ? <Badge color="gray">untyped</Badge> : <Badge>{event.type}</Badge>}
            {!event.subject_valid && (
              <Tooltip label="Subject does not follow evt.<domain>.<entity>.<action>[.<key>]">
                <Badge color="orange">non-conforming subject</Badge>
              </Tooltip>
            )}
            {event.blob && <Badge color="violet">blob</Badge>}
          </Group>
          <Table withRowBorders={false} verticalSpacing={2}>
            <Table.Tbody>
              <Row k="subject" v={event.subject} copy />
              <Row k="sequence" v={event.seq} />
              <Row k="stored" v={formatTime(event.stream_time)} />
              <Row k="ce-time" v={formatTime(event.event_time)} />
              <Row k="id" v={event.id} copy />
              <Row k="source" v={event.source} />
              <Row k="size" v={`${event.size} bytes`} mono={false} />
              {event.blob && <Row k="blob" v={`${event.blob.bucket}/${event.blob.name} (${event.blob.size} bytes)`} />}
            </Table.Tbody>
          </Table>
          <Group gap={6} wrap="nowrap">
            <Text size="xs" c="dimmed">
              Replay from here
            </Text>
            <Text size="xs" ff="monospace" truncate>
              {replay}
            </Text>
            <Copy value={replay} label="Copy replay command" />
          </Group>
          <Divider label="Payload" labelPosition="left" />
          <PayloadView data={event.data} encoding={event.encoding} contentType={event.content_type} />
          {extra.length > 0 && (
            <>
              <Divider label="Other headers" labelPosition="left" />
              <Table withRowBorders={false} verticalSpacing={2}>
                <Table.Tbody>
                  {extra.map(([k, v]) => (
                    <Row key={k} k={k} v={(v ?? []).join(', ')} />
                  ))}
                </Table.Tbody>
              </Table>
            </>
          )}
        </Stack>
      )}
    </Drawer>
  )
}
