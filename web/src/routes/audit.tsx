import { Badge, Button, Code, Group, Paper, Select, Text, TextInput, Tooltip } from '@mantine/core'
import { IconFilter } from '@tabler/icons-react'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useMemo, useState } from 'react'
import type { AuditEntry } from '@/api/client'
import { useAudit, type AuditQuery } from '@/api/queries'
import { ErrorAlert } from '@/components/ErrorAlert'
import { PageHeader } from '@/components/PageHeader'
import { VirtualTable, type Column } from '@/components/VirtualTable'
import { formatTime } from '@/lib/format'

export const Route = createFileRoute('/audit')({
  validateSearch: (s: Record<string, unknown>): AuditQuery => {
    const str = (v: unknown) => (typeof v === 'string' && v.trim() ? v.trim() : undefined)
    const r = str(s.result)
    const iso = (v: unknown) => {
      const t = str(v)
      return t && !Number.isNaN(Date.parse(t)) ? new Date(t).toISOString() : undefined
    }
    return {
      action: str(s.action),
      actor: str(s.actor),
      target: str(s.target),
      result: r === 'ok' || r === 'denied' || r === 'error' ? r : undefined,
      since: iso(s.since),
      until: iso(s.until),
    }
  },
  component: Audit,
})

const columns: Column<AuditEntry>[] = [
  { key: 'time', header: 'Time', width: '190px', render: (e) => <Text size="sm" ff="monospace">{formatTime(e.time)}</Text> },
  { key: 'action', header: 'Action', width: '160px', render: (e) => <Text size="sm" ff="monospace">{e.action}</Text> },
  {
    key: 'result',
    header: 'Result',
    width: '80px',
    render: (e) => (
      <Badge size="sm" color={e.result === 'ok' ? 'teal' : e.result === 'denied' ? 'orange' : 'red'} variant="light">
        {e.result}
      </Badge>
    ),
  },
  {
    key: 'actor',
    header: 'Actor',
    width: 'minmax(180px, 1fr)',
    render: (e) => (
      <Text size="sm" truncate title={e.actor}>
        {e.actor}
      </Text>
    ),
  },
  { key: 'target', header: 'Target', width: 'minmax(160px, 1fr)', render: (e) => <Text size="sm" ff="monospace" truncate>{e.target}</Text> },
  { key: 'ip', header: 'IP', width: '130px', render: (e) => <Text size="sm" ff="monospace">{e.ip || '—'}</Text> },
  {
    key: 'details',
    header: 'Details',
    width: 'minmax(200px, 2fr)',
    render: (e) => {
      const d = e.details && Object.keys(e.details).length ? JSON.stringify(e.details) : ''
      return (
        <Tooltip label={<Code block>{JSON.stringify(e.details, null, 2)}</Code>} disabled={!d} multiline maw={500}>
          <Text size="sm" ff="monospace" c="dimmed" truncate>
            {d || '—'}
          </Text>
        </Tooltip>
      )
    },
  },
]

const presets: { value: string; label: string; ms?: number }[] = [
  { value: 'any', label: 'Any time' },
  { value: '1h', label: 'Last hour', ms: 3_600_000 },
  { value: '24h', label: 'Last 24 hours', ms: 86_400_000 },
  { value: '7d', label: 'Last 7 days', ms: 7 * 86_400_000 },
  { value: '30d', label: 'Last 30 days', ms: 30 * 86_400_000 },
  { value: 'custom', label: 'Custom range' },
]

/** ISO → datetime-local value (local time). */
function toLocal(iso?: string): string {
  if (!iso) return ''
  const d = new Date(iso)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`
}

function Audit() {
  const search = Route.useSearch()
  const navigate = useNavigate({ from: Route.fullPath })
  const [draft, setDraft] = useState<AuditQuery>(search)
  const [range, setRange] = useState(search.since || search.until ? 'custom' : 'any')
  const audit = useAudit(search)
  const rows = useMemo(() => audit.data?.pages.flatMap((p) => p.entries) ?? [], [audit.data])
  return (
    <>
      <PageHeader title="Audit" description="Every admin change and security-relevant action, newest first." />
      <Paper withBorder p="sm" mb="sm">
        <form
          onSubmit={(e) => {
            e.preventDefault()
            const p = presets.find((x) => x.value === range)
            const next = { ...draft }
            if (range === 'any') next.since = next.until = undefined
            else if (p?.ms) {
              next.since = new Date(Date.now() - p.ms).toISOString()
              next.until = undefined
            }
            void navigate({ search: next })
          }}
        >
          <Group align="flex-end" gap="xs">
            <TextInput label="Action" placeholder="client. (prefix) or client.revoke" value={draft.action ?? ''} onChange={(e) => setDraft({ ...draft, action: e.currentTarget.value || undefined })} />
            <TextInput label="Actor" value={draft.actor ?? ''} onChange={(e) => setDraft({ ...draft, actor: e.currentTarget.value || undefined })} />
            <TextInput label="Target" value={draft.target ?? ''} onChange={(e) => setDraft({ ...draft, target: e.currentTarget.value || undefined })} />
            <Select
              label="Result"
              w={110}
              clearable
              data={['ok', 'denied', 'error']}
              value={draft.result ?? null}
              onChange={(v) => setDraft({ ...draft, result: (v as AuditQuery['result']) ?? undefined })}
            />
            <Select label="Time" w={160} data={presets} value={range} onChange={(v) => setRange(v ?? 'any')} allowDeselect={false} />
            {range === 'custom' && (
              <>
                <TextInput
                  label="From"
                  type="datetime-local"
                  value={toLocal(draft.since)}
                  onChange={(e) => setDraft({ ...draft, since: e.currentTarget.value ? new Date(e.currentTarget.value).toISOString() : undefined })}
                />
                <TextInput
                  label="To"
                  type="datetime-local"
                  value={toLocal(draft.until)}
                  onChange={(e) => setDraft({ ...draft, until: e.currentTarget.value ? new Date(e.currentTarget.value).toISOString() : undefined })}
                />
              </>
            )}
            <Button type="submit" leftSection={<IconFilter size={14} />}>
              Apply
            </Button>
          </Group>
        </form>
      </Paper>
      <ErrorAlert error={audit.error} />
      <VirtualTable
        testId="audit-table"
        rows={rows}
        columns={columns}
        rowKey={(e) => e.id}
        height="calc(100vh - 300px)"
        empty={<Text size="sm" c="dimmed">{audit.isLoading ? 'Loading…' : 'No audit entries match'}</Text>}
        footer={
          <Group justify="space-between">
            <Text size="xs" c="dimmed">
              {rows.length} entries
            </Text>
            <Button size="xs" variant="default" disabled={!audit.hasNextPage} loading={audit.isFetchingNextPage} onClick={() => void audit.fetchNextPage()}>
              Load older
            </Button>
          </Group>
        }
      />
    </>
  )
}
