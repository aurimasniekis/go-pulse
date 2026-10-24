import { ActionIcon, Anchor, Badge, Button, Card, Group, SimpleGrid, Stack, Table, Text, Title, Tooltip } from '@mantine/core'
import { IconEdit, IconListSearch, IconTrash } from '@tabler/icons-react'
import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useState, type ReactNode } from 'react'
import type { ConsumerDTO } from '@/api/client'
import { useConsumers, useStream, useStreamMutations } from '@/api/queries'
import { ConfirmModal } from '@/components/ConfirmModal'
import { ErrorAlert } from '@/components/ErrorAlert'
import { PageHeader } from '@/components/PageHeader'
import { Can } from '@/components/Can'
import { StreamForm } from '@/components/StreamForm'
import { formatBytes, formatDuration, formatNumber, formatTime, timeAgo } from '@/lib/format'
import { notifyErr, notifyOk } from '@/lib/notify'

export const Route = createFileRoute('/streams/$name')({ component: StreamDetail })

function Stat({ label, value }: { label: string; value: ReactNode }) {
  return (
    <div>
      <Text size="xs" c="dimmed" tt="uppercase" fw={600}>
        {label}
      </Text>
      <Text size="lg" fw={600}>
        {value}
      </Text>
    </div>
  )
}

function KV({ k, v }: { k: string; v: ReactNode }) {
  return (
    <Table.Tr>
      <Table.Td w={170}>
        <Text size="xs" c="dimmed">
          {k}
        </Text>
      </Table.Td>
      <Table.Td>
        <Text size="sm">{v}</Text>
      </Table.Td>
    </Table.Tr>
  )
}

function StreamDetail() {
  const { name } = Route.useParams()
  const navigate = useNavigate()
  const stream = useStream(name)
  const consumers = useConsumers(name)
  const { remove, removeConsumer } = useStreamMutations()
  const [editing, setEditing] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [delConsumer, setDelConsumer] = useState<ConsumerDTO | null>(null)
  const s = stream.data

  return (
    <>
      <PageHeader
        title={name}
        description={s?.description || undefined}
        actions={
          <>
            <Button
              variant="default"
              leftSection={<IconListSearch size={14} />}
              onClick={() => void navigate({ to: '/explorer', search: name === 'EVENTS' ? {} : { stream: name } })}
            >
              Explore
            </Button>
            <Can perm="streams.write">
            <Button variant="default" leftSection={<IconEdit size={14} />} disabled={!s || s.managed || s.kind !== 'stream'} onClick={() => setEditing(true)}>
              Edit
            </Button>
            <Button color="red" variant="light" leftSection={<IconTrash size={14} />} disabled={!s || s.managed} onClick={() => setDeleting(true)}>
              Delete
            </Button>
            </Can>
          </>
        }
      />
      <ErrorAlert error={stream.error} />
      {s && (
        <Stack gap="md">
          <Card>
            <SimpleGrid cols={{ base: 2, md: 6 }}>
              <Stat label="Messages" value={formatNumber(s.state.msgs)} />
              <Stat label="Size" value={formatBytes(s.state.bytes)} />
              <Stat label="Limit" value={formatBytes(s.max_bytes)} />
              <Stat label="Subjects" value={formatNumber(s.state.num_subjects)} />
              <Stat label="Consumers" value={s.state.consumers} />
              <Stat label="Last message" value={s.state.msgs ? timeAgo(s.state.last_time) : '—'} />
            </SimpleGrid>
          </Card>
          <SimpleGrid cols={{ base: 1, md: 2 }}>
            <Card>
              <Title order={5} mb="xs">
                Configuration
              </Title>
              <Table withRowBorders={false} verticalSpacing={2}>
                <Table.Tbody>
                  <KV k="Subjects" v={<span style={{ fontFamily: 'var(--mantine-font-family-monospace)' }}>{s.subjects.join(', ') || '—'}</span>} />
                  <KV k="Storage" v={s.storage} />
                  <KV k="Retention" v={s.retention} />
                  <KV k="When full" v={s.discard} />
                  <KV k="Max age" v={formatDuration(s.max_age_seconds)} />
                  <KV k="Max messages" v={formatNumber(s.max_msgs)} />
                  <KV k="Max message size" v={formatBytes(s.max_msg_size)} />
                  <KV k="Compression" v={s.compression} />
                  <KV k="Dedupe window" v={formatDuration(s.duplicates_seconds)} />
                  <KV k="Direct get" v={s.allow_direct ? 'yes' : 'no'} />
                  <KV k="Created" v={formatTime(s.created)} />
                </Table.Tbody>
              </Table>
            </Card>
            <Card>
              <Title order={5} mb="xs">
                State
              </Title>
              <Table withRowBorders={false} verticalSpacing={2}>
                <Table.Tbody>
                  <KV k="First sequence" v={formatNumber(s.state.first_seq)} />
                  <KV k="First message" v={formatTime(s.state.first_time)} />
                  <KV k="Last sequence" v={formatNumber(s.state.last_seq)} />
                  <KV k="Last message" v={formatTime(s.state.last_time)} />
                  <KV k="Deleted (interior)" v={formatNumber(s.state.num_deleted)} />
                </Table.Tbody>
              </Table>
            </Card>
          </SimpleGrid>
          <Card>
            <Title order={5} mb="xs">
              Consumers
            </Title>
            <ErrorAlert error={consumers.error} />
            <Table.ScrollContainer minWidth={800}>
              <Table data-testid="consumers-table">
                <Table.Thead>
                  <Table.Tr>
                    <Table.Th>Name</Table.Th>
                    <Table.Th>Filter</Table.Th>
                    <Table.Th>Deliver</Table.Th>
                    <Table.Th ta="right">Pending</Table.Th>
                    <Table.Th ta="right">Ack pending</Table.Th>
                    <Table.Th ta="right">Redelivered</Table.Th>
                    <Table.Th>Last active</Table.Th>
                    <Table.Th />
                  </Table.Tr>
                </Table.Thead>
                <Table.Tbody>
                  {(consumers.data ?? []).length === 0 && (
                    <Table.Tr>
                      <Table.Td colSpan={8}>
                        <Text size="sm" c="dimmed">
                          No consumers
                        </Text>
                      </Table.Td>
                    </Table.Tr>
                  )}
                  {(consumers.data ?? []).map((c) => (
                    <Table.Tr key={c.name}>
                      <Table.Td>
                        <Group gap={6} wrap="nowrap">
                          <Text size="sm" ff="monospace">
                            {c.name}
                          </Text>
                          {c.durable && <Badge size="xs">durable</Badge>}
                          {c.internal && (
                            <Badge size="xs" color="gray">
                              pulsed
                            </Badge>
                          )}
                          {c.paused && (
                            <Badge size="xs" color="yellow">
                              paused
                            </Badge>
                          )}
                        </Group>
                      </Table.Td>
                      <Table.Td>
                        <Text size="sm" ff="monospace">
                          {c.filter_subjects.join(', ') || '(all)'}
                        </Text>
                      </Table.Td>
                      <Table.Td>{c.deliver_policy}</Table.Td>
                      <Table.Td ta="right">{formatNumber(c.num_pending)}</Table.Td>
                      <Table.Td ta="right">{formatNumber(c.num_ack_pending)}</Table.Td>
                      <Table.Td ta="right">{formatNumber(c.num_redelivered)}</Table.Td>
                      <Table.Td>{c.last_active ? timeAgo(c.last_active) : '—'}</Table.Td>
                      <Table.Td>
                        <Can perm="streams.write">
                        <Tooltip label="Delete consumer">
                          <ActionIcon color="red" onClick={() => setDelConsumer(c)} aria-label={`Delete consumer ${c.name}`}>
                            <IconTrash size={14} />
                          </ActionIcon>
                        </Tooltip>
                        </Can>
                      </Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            </Table.ScrollContainer>
          </Card>
          <Anchor size="sm" renderRoot={(props) => <Link {...props} to="/streams" />}>
            ← All streams
          </Anchor>
        </Stack>
      )}
      {s && <StreamForm opened={editing} onClose={() => setEditing(false)} stream={s} />}
      <ConfirmModal
        opened={deleting}
        onClose={() => setDeleting(false)}
        title={`Delete stream ${name}`}
        confirmText={name}
        loading={remove.isPending}
        onConfirm={async () => {
          try {
            await remove.mutateAsync(name)
            notifyOk(`Stream ${name} deleted`)
            void navigate({ to: '/streams' })
          } catch (e) {
            notifyErr(e)
          }
        }}
      >
        <Text size="sm">This deletes the stream and every message in it. It cannot be undone.</Text>
      </ConfirmModal>
      <ConfirmModal
        opened={delConsumer !== null}
        onClose={() => setDelConsumer(null)}
        title={`Delete consumer ${delConsumer?.name ?? ''}`}
        confirmText={delConsumer?.name ?? ''}
        loading={removeConsumer.isPending}
        onConfirm={async () => {
          if (!delConsumer) return
          try {
            await removeConsumer.mutateAsync({ stream: name, consumer: delConsumer.name })
            notifyOk(`Consumer ${delConsumer.name} deleted`)
            setDelConsumer(null)
          } catch (e) {
            notifyErr(e)
          }
        }}
      >
        <Text size="sm">A durable consumer loses its position; the owning client recreates it on its next subscribe.</Text>
      </ConfirmModal>
    </>
  )
}
