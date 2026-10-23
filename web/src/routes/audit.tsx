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
    return {
      action: str(s.action),
      actor: str(s.actor),
      target: str(s.target),
      result: r === 'ok' || r === 'denied' || r === 'error' ? r : undefined,
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
  { key: 'actor', header: 'Actor', width: '120px', render: (e) => <Text size="sm">{e.actor}</Text> },
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

function Audit() {
  const search = Route.useSearch()
  const navigate = useNavigate({ from: Route.fullPath })
  const [draft, setDraft] = useState<AuditQuery>(search)
  const audit = useAudit(search)
  const rows = useMemo(() => audit.data?.pages.flatMap((p) => p.entries) ?? [], [audit.data])
  return (
    <>
      <PageHeader title="Audit" description="Every admin change and security-relevant action, newest first." />
      <Paper withBorder p="sm" mb="sm">
        <form
          onSubmit={(e) => {
            e.preventDefault()
            void navigate({ search: draft })
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
