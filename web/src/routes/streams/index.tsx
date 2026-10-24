import { Anchor, Badge, Button, Group, Table, Text, Tooltip } from '@mantine/core'
import { IconPlus } from '@tabler/icons-react'
import { createFileRoute, Link } from '@tanstack/react-router'
import { useState } from 'react'
import { useStreams } from '@/api/queries'
import { ErrorAlert } from '@/components/ErrorAlert'
import { PageHeader } from '@/components/PageHeader'
import { Can } from '@/components/Can'
import { StreamForm } from '@/components/StreamForm'
import { formatBytes, formatNumber, timeAgo } from '@/lib/format'

export const Route = createFileRoute('/streams/')({ component: Streams })

function Streams() {
  const streams = useStreams()
  const [creating, setCreating] = useState(false)
  return (
    <>
      <PageHeader
        title="Streams"
        description="JetStream streams in the APP account. EVENTS and the blobs store are managed by pulsed."
        actions={
          <Can perm="streams.write">
            <Button leftSection={<IconPlus size={14} />} onClick={() => setCreating(true)}>
              Create stream
            </Button>
          </Can>
        }
      />
      <ErrorAlert error={streams.error} />
      <Table.ScrollContainer minWidth={800}>
        <Table striped data-testid="streams-table">
          <Table.Thead>
            <Table.Tr>
              <Table.Th>Name</Table.Th>
              <Table.Th>Subjects</Table.Th>
              <Table.Th ta="right">Messages</Table.Th>
              <Table.Th ta="right">Size</Table.Th>
              <Table.Th ta="right">Limit</Table.Th>
              <Table.Th ta="right">Subjects #</Table.Th>
              <Table.Th ta="right">Consumers</Table.Th>
              <Table.Th>Last message</Table.Th>
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {(streams.data ?? []).map((s) => (
              <Table.Tr key={s.name}>
                <Table.Td>
                  <Group gap={6} wrap="nowrap">
                    <Anchor size="sm" fw={500} renderRoot={(props) => <Link {...props} to="/streams/$name" params={{ name: s.name }} />}>
                      {s.name}
                    </Anchor>
                    {s.kind !== 'stream' && (
                      <Badge size="xs" color="grape">
                        {s.kind}
                      </Badge>
                    )}
                    {s.managed && (
                      <Tooltip label="Managed by pulsed">
                        <Badge size="xs" color="indigo" variant="outline">
                          managed
                        </Badge>
                      </Tooltip>
                    )}
                  </Group>
                </Table.Td>
                <Table.Td>
                  <Text size="sm" ff="monospace" truncate maw={260}>
                    {s.subjects.join(', ')}
                  </Text>
                </Table.Td>
                <Table.Td ta="right">{formatNumber(s.state.msgs)}</Table.Td>
                <Table.Td ta="right">{formatBytes(s.state.bytes)}</Table.Td>
                <Table.Td ta="right">{formatBytes(s.max_bytes)}</Table.Td>
                <Table.Td ta="right">{formatNumber(s.state.num_subjects)}</Table.Td>
                <Table.Td ta="right">{s.state.consumers}</Table.Td>
                <Table.Td>
                  <Text size="sm" c="dimmed">
                    {s.state.msgs ? timeAgo(s.state.last_time) : '—'}
                  </Text>
                </Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      </Table.ScrollContainer>
      <StreamForm opened={creating} onClose={() => setCreating(false)} />
    </>
  )
}
