import { Badge, Button, Group, NumberInput, Paper, SegmentedControl, Select, SimpleGrid, Stack, TagsInput, Text, TextInput, Tooltip } from '@mantine/core'
import { IconFilter, IconRefresh, IconSend, IconX } from '@tabler/icons-react'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useEffect, useMemo, useState } from 'react'
import type { EventDTO } from '@/api/client'
import { useEvents, useStreams, type EventsQuery } from '@/api/queries'
import { ErrorAlert } from '@/components/ErrorAlert'
import { EventDrawer } from '@/components/EventDrawer'
import { eventColumns } from '@/components/eventColumns'
import { PageHeader } from '@/components/PageHeader'
import { Can } from '@/components/Can'
import { PublishModal } from '@/components/PublishModal'
import { VirtualTable } from '@/components/VirtualTable'
import { formatNumber } from '@/lib/format'
import { parseEventsSearch } from '@/lib/search'

export const Route = createFileRoute('/explorer')({
  validateSearch: (s: Record<string, unknown>): EventsQuery => parseEventsSearch(s),
  component: Explorer,
})

interface Draft {
  stream: string
  subject: string[]
  dir: 'forward' | 'backward'
  start_seq: number | ''
  start_time: string
  type: string
  header: string[]
  contains: string
  json_path: string
  json_value: string
}

function toDraft(q: EventsQuery): Draft {
  return {
    stream: q.stream ?? 'EVENTS',
    subject: q.subject ?? [],
    dir: q.dir ?? 'backward',
    start_seq: q.start_seq ?? '',
    start_time: q.start_time ?? '',
    type: q.type ?? '',
    header: q.header ?? [],
    contains: q.contains ?? '',
    json_path: q.json_path ?? '',
    json_value: q.json_value ?? '',
  }
}

function toSearch(d: Draft): EventsQuery {
  return parseEventsSearch({
    ...d,
    stream: d.stream === 'EVENTS' ? undefined : d.stream,
    dir: d.dir === 'backward' ? undefined : d.dir,
    start_seq: d.start_seq === '' ? undefined : d.start_seq,
    start_time: d.dir === 'forward' && d.start_time && d.start_seq === '' ? new Date(d.start_time).toISOString() : undefined,
  })
}

function Explorer() {
  const search = Route.useSearch()
  const navigate = useNavigate({ from: Route.fullPath })
  const [draft, setDraft] = useState<Draft>(() => toDraft(search))
  useEffect(() => setDraft(toDraft(search)), [search])
  const set = <K extends keyof Draft>(k: K, v: Draft[K]) => setDraft((d) => ({ ...d, [k]: v }))
  const streams = useStreams()
  const query = useMemo(() => ({ ...search, limit: 100 }), [search])
  const events = useEvents(query)
  const [selected, setSelected] = useState<EventDTO | null>(null)
  const [publishing, setPublishing] = useState(false)
  const rows = useMemo(() => events.data?.pages.flatMap((p) => p.events) ?? [], [events.data])
  const last = events.data?.pages.at(-1)
  const scanned = events.data?.pages.reduce((n, p) => n + p.scanned, 0) ?? 0

  return (
    <>
      <PageHeader
        title="Explorer"
        description="Browse stored events. Filters on type, headers and payload run in pulsed within a scan budget."
        actions={
          <>
          <Can perm="events.publish">
            <Button variant="default" leftSection={<IconSend size={14} />} onClick={() => setPublishing(true)}>
              Publish event
            </Button>
          </Can>
          <Button
            leftSection={<IconRefresh size={14} />}
            variant="default"
            onClick={() => events.refetch()}
            loading={events.isFetching && !events.isFetchingNextPage}
          >
            Refresh
          </Button>
          </>
        }
      />
      <Paper withBorder p="sm" mb="sm">
        <form
          onSubmit={(e) => {
            e.preventDefault()
            void navigate({ search: toSearch(draft) })
          }}
        >
          <Stack gap="xs">
            <SimpleGrid cols={{ base: 1, sm: 2, lg: 4 }} spacing="xs">
              <Select
                label="Stream"
                data={(streams.data ?? []).map((s) => s.name)}
                value={draft.stream}
                onChange={(v) => set('stream', v ?? 'EVENTS')}
                searchable
                allowDeselect={false}
              />
              <TagsInput
                label="Subjects"
                placeholder="evt.home.> (Enter to add)"
                value={draft.subject}
                onChange={(v) => set('subject', v)}
                clearable
              />
              <TextInput
                label="Type"
                placeholder="home.door.opened.v1 or (untyped)"
                value={draft.type}
                onChange={(e) => set('type', e.currentTarget.value)}
              />
              <Stack gap={4}>
                <Text size="xs" fw={500}>
                  Order
                </Text>
                <SegmentedControl
                  fullWidth
                  value={draft.dir}
                  onChange={(v) => set('dir', v as Draft['dir'])}
                  data={[
                    { label: 'Newest first', value: 'backward' },
                    { label: 'Oldest first', value: 'forward' },
                  ]}
                />
              </Stack>
              <TextInput label="Payload contains" value={draft.contains} onChange={(e) => set('contains', e.currentTarget.value)} />
              <Group grow gap="xs" align="flex-end">
                <TextInput label="JSON path" placeholder="door.name" value={draft.json_path} onChange={(e) => set('json_path', e.currentTarget.value)} />
                <TextInput label="equals" value={draft.json_value} onChange={(e) => set('json_value', e.currentTarget.value)} />
              </Group>
              <TagsInput label="Headers" placeholder="key=value" value={draft.header} onChange={(v) => set('header', v)} clearable />
              <Group grow gap="xs" align="flex-end">
                <NumberInput
                  label={draft.dir === 'forward' ? 'From seq' : 'Up to seq'}
                  min={1}
                  value={draft.start_seq}
                  onChange={(v) => set('start_seq', typeof v === 'number' ? v : '')}
                  allowDecimal={false}
                />
                <TextInput
                  label="From time"
                  type="datetime-local"
                  step="1"
                  title={draft.dir === 'forward' ? undefined : 'Oldest-first only'}
                  disabled={draft.dir !== 'forward' || draft.start_seq !== ''}
                  value={draft.start_time ? toLocalInput(draft.start_time) : ''}
                  onChange={(e) => set('start_time', e.currentTarget.value)}
                />
              </Group>
            </SimpleGrid>
            <Group justify="flex-end" gap="xs">
              <Button variant="subtle" color="gray" leftSection={<IconX size={14} />} onClick={() => void navigate({ search: {} })}>
                Reset
              </Button>
              <Button type="submit" leftSection={<IconFilter size={14} />}>
                Apply
              </Button>
            </Group>
          </Stack>
        </form>
      </Paper>
      <ErrorAlert error={events.error} />
      <VirtualTable
        testId="events-table"
        rows={rows}
        columns={eventColumns}
        rowKey={(e) => `${e.stream}:${e.seq}`}
        selectedKey={selected ? `${selected.stream}:${selected.seq}` : undefined}
        onRowClick={setSelected}
        height="calc(100vh - 430px)"
        empty={
          <Text c="dimmed" size="sm">
            {events.isLoading ? 'Loading…' : 'No events match these filters'}
          </Text>
        }
        footer={
          <Group justify="space-between">
            <Group gap="xs">
              <Text size="xs" c="dimmed" data-testid="events-summary">
                {formatNumber(rows.length)} events · scanned {formatNumber(scanned)}
                {last && last.last_seq > 0 ? ` · stream seq ${formatNumber(last.first_seq)}–${formatNumber(last.last_seq)}` : ''}
              </Text>
              {last?.partial && (
                <Tooltip label="The scan budget ran out before the page filled. Continue to scan further.">
                  <Badge color="yellow" variant="light">
                    budget: {last.reason}
                  </Badge>
                </Tooltip>
              )}
            </Group>
            <Button
              size="xs"
              variant="default"
              disabled={!events.hasNextPage}
              loading={events.isFetchingNextPage}
              onClick={() => void events.fetchNextPage()}
            >
              {last?.partial ? 'Continue scan' : 'Load more'}
            </Button>
          </Group>
        }
      />
      <EventDrawer event={selected} onClose={() => setSelected(null)} />
      <PublishModal opened={publishing} onClose={() => setPublishing(false)} />
    </>
  )
}

/** ISO (UTC) or a datetime-local value → the datetime-local input format in local time. */
function toLocalInput(v: string): string {
  if (!v.endsWith('Z')) return v.slice(0, 19)
  const d = new Date(v)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}
