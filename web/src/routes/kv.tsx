import { ActionIcon, Badge, Button, Drawer, FileButton, Group, Modal, NumberInput, Paper, Select, Stack, Table, Tabs, Text, Textarea, TextInput, Tooltip } from '@mantine/core'
import { useForm } from '@mantine/form'
import { IconDownload, IconKey, IconPlus, IconTrash, IconUpload } from '@tabler/icons-react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useState } from 'react'
import { admin, apiError, unwrap, type Schemas } from '@/api/client'
import { sessionFetch } from '@/api/session'
import { useBuckets, useKVEntry, useKVKeys, useObjects } from '@/api/queries'
import { ConfirmModal } from '@/components/ConfirmModal'
import { ErrorAlert } from '@/components/ErrorAlert'
import { PageHeader } from '@/components/PageHeader'
import { Can } from '@/components/Can'
import { PayloadView } from '@/components/PayloadView'
import { VirtualTable, type Column } from '@/components/VirtualTable'
import { formatBytes, formatNumber, formatTime, timeAgo } from '@/lib/format'
import { notifyErr, notifyOk } from '@/lib/notify'

interface KVSearch {
  tab?: 'kv' | 'objects'
  bucket?: string
}

export const Route = createFileRoute('/kv')({
  validateSearch: (s: Record<string, unknown>): KVSearch => ({
    tab: s.tab === 'objects' ? 'objects' : undefined,
    bucket: typeof s.bucket === 'string' && s.bucket ? s.bucket : undefined,
  }),
  component: KVPage,
})

type KVKey = Schemas['KVKey']
type ObjectEntry = Schemas['ObjectEntry']

const keyColumns: Column<KVKey>[] = [
  { key: 'key', header: 'Key', width: 'minmax(200px, 2fr)', render: (k) => <Text size="sm" ff="monospace" truncate>{k.key}</Text> },
  { key: 'rev', header: 'Revision', width: '90px', align: 'right', render: (k) => <Text size="sm" ff="monospace">{k.revision}</Text> },
  { key: 'created', header: 'Updated', width: '190px', render: (k) => <Text size="sm" ff="monospace">{formatTime(k.created)}</Text> },
]

function CreateBucketModal({ kind, onClose }: { kind: 'kv' | 'objects'; onClose: () => void }) {
  const qc = useQueryClient()
  const form = useForm({
    initialValues: { name: '', description: '', history: 1, ttl: 0 },
    validate: { name: (v) => (/^[A-Za-z0-9_-]{1,64}$/.test(v) ? null : 'Letters, digits, _ and -, up to 64') },
  })
  const create = useMutation({
    mutationFn: async (v: typeof form.values) =>
      kind === 'kv'
        ? unwrap(await admin.POST('/kv', { body: { name: v.name, description: v.description || undefined, history: v.history, ttl_seconds: v.ttl } }))
        : unwrap(await admin.POST('/objects', { body: { name: v.name, description: v.description || undefined, ttl_seconds: v.ttl } })),
    onSuccess: (b) => {
      notifyOk(`Bucket ${b.name} created`)
      void qc.invalidateQueries({ queryKey: ['buckets'] })
      onClose()
    },
    onError: notifyErr,
  })
  return (
    <Modal opened onClose={onClose} title={kind === 'kv' ? 'Create KV bucket' : 'Create object store'}>
      <form onSubmit={form.onSubmit((v) => create.mutate(v))}>
        <Stack gap="sm">
          <TextInput label="Name" required {...form.getInputProps('name')} />
          <TextInput label="Description" {...form.getInputProps('description')} />
          {kind === 'kv' && <NumberInput label="History per key" min={1} max={64} {...form.getInputProps('history')} />}
          <NumberInput label="TTL (seconds, 0 = keep forever)" min={0} {...form.getInputProps('ttl')} />
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" loading={create.isPending}>
              Create
            </Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  )
}

function PutKeyModal({ bucket, initialKey, onClose }: { bucket: string; initialKey?: string; onClose: () => void }) {
  const qc = useQueryClient()
  const form = useForm({ initialValues: { key: initialKey ?? '', value: '' }, validate: { key: (v) => (/^[-/_=.a-zA-Z0-9]+$/.test(v) ? null : 'Letters, digits and - / _ = .') } })
  const put = useMutation({
    mutationFn: async (v: typeof form.values) =>
      unwrap(await admin.PUT('/kv/{bucket}/entry', { params: { path: { bucket }, query: { key: v.key } }, body: { value: v.value, encoding: 'utf8' } })),
    onSuccess: (k) => {
      notifyOk(`${k.key} set (revision ${k.revision})`)
      void qc.invalidateQueries({ queryKey: ['kv', bucket] })
      void qc.invalidateQueries({ queryKey: ['buckets'] })
      onClose()
    },
    onError: notifyErr,
  })
  return (
    <Modal opened onClose={onClose} title={initialKey ? `Set ${initialKey}` : `New key in ${bucket}`} size="lg">
      <form onSubmit={form.onSubmit((v) => put.mutate(v))}>
        <Stack gap="sm">
          <TextInput label="Key" required disabled={!!initialKey} {...form.getInputProps('key')} />
          <Textarea label="Value" autosize minRows={4} styles={{ input: { fontFamily: 'var(--mantine-font-family-monospace)' } }} {...form.getInputProps('value')} />
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" loading={put.isPending}>
              Save
            </Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  )
}

function KVTab({ bucket }: { bucket: string }) {
  const qc = useQueryClient()
  const [filter, setFilter] = useState('')
  const [applied, setApplied] = useState('')
  const keys = useKVKeys(bucket, applied)
  const [open, setOpen] = useState<string | null>(null)
  const [putting, setPutting] = useState<{ key?: string } | null>(null)
  const entry = useKVEntry(bucket, open)
  const del = useMutation({
    mutationFn: async (key: string) => {
      const r = await admin.DELETE('/kv/{bucket}/entry', { params: { path: { bucket }, query: { key } } })
      if (r.error) unwrap(r)
    },
    onSuccess: () => {
      notifyOk('Key deleted')
      setOpen(null)
      void qc.invalidateQueries({ queryKey: ['kv', bucket] })
    },
    onError: notifyErr,
  })
  const rows = keys.data?.keys ?? []
  return (
    <>
      <Group mb="xs" gap="xs" align="flex-end">
        <form
          onSubmit={(e) => {
            e.preventDefault()
            setApplied(filter.trim())
          }}
          style={{ flex: 1 }}
        >
          <TextInput label="Key filter" placeholder="ui.* or devices.>" value={filter} onChange={(e) => setFilter(e.currentTarget.value)} />
        </form>
        <Can perm="streams.write">
          <Button leftSection={<IconPlus size={14} />} onClick={() => setPutting({})}>
            New key
          </Button>
        </Can>
      </Group>
      <ErrorAlert error={keys.error} />
      <VirtualTable
        testId="kv-keys"
        rows={rows}
        columns={keyColumns}
        rowKey={(k) => k.key}
        selectedKey={open ?? undefined}
        onRowClick={(k) => setOpen(k.key)}
        height="calc(100vh - 360px)"
        empty={
          <Text size="sm" c="dimmed">
            {keys.isLoading ? 'Loading…' : 'No keys'}
          </Text>
        }
        footer={
          <Text size="xs" c="dimmed">
            {formatNumber(rows.length)} keys{keys.data?.truncated ? ' (more exist: narrow the filter)' : ''}
          </Text>
        }
      />
      <Drawer opened={!!open} onClose={() => setOpen(null)} title={open} position="right" size="lg">
        {entry.data && (
          <Stack gap="sm">
            <Group justify="space-between">
              <Text size="sm" c="dimmed">
                revision {entry.data.revision} · {timeAgo(entry.data.created)}
              </Text>
              <Can perm="streams.write">
              <Group gap="xs">
                <Button size="xs" variant="default" leftSection={<IconKey size={12} />} onClick={() => setPutting({ key: open! })}>
                  Set value
                </Button>
                <Button size="xs" color="red" variant="light" leftSection={<IconTrash size={12} />} loading={del.isPending} onClick={() => del.mutate(open!)}>
                  Delete
                </Button>
              </Group>
              </Can>
            </Group>
            <PayloadView data={entry.data.value} encoding={entry.data.encoding} />
            <Text size="sm" fw={500}>
              History
            </Text>
            <Table verticalSpacing={2}>
              <Table.Tbody>
                {entry.data.history.map((h) => (
                  <Table.Tr key={h.revision}>
                    <Table.Td>
                      <Text size="xs" ff="monospace">
                        {h.revision}
                      </Text>
                    </Table.Td>
                    <Table.Td>
                      <Badge size="xs" variant="light" color={h.operation === 'put' ? 'gray' : 'orange'}>
                        {h.operation}
                      </Badge>
                    </Table.Td>
                    <Table.Td>
                      <Text size="xs" ff="monospace">
                        {formatTime(h.created)}
                      </Text>
                    </Table.Td>
                    <Table.Td ta="right">
                      <Text size="xs">{formatBytes(h.size)}</Text>
                    </Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </Stack>
        )}
      </Drawer>
      {putting && <PutKeyModal bucket={bucket} initialKey={putting.key} onClose={() => setPutting(null)} />}
    </>
  )
}

function ObjectsTab({ bucket, managed }: { bucket: string; managed: boolean }) {
  const qc = useQueryClient()
  const objs = useObjects(bucket)
  const [deleting, setDeleting] = useState<ObjectEntry | null>(null)
  const upload = useMutation({
    mutationFn: async (file: File) => {
      const r = await sessionFetch(`/api/admin/v1/objects/${encodeURIComponent(bucket)}/upload?name=${encodeURIComponent(file.name)}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/octet-stream' },
        body: file,
      })
      if (!r.ok) throw apiError(await r.json().catch(() => ({})), r.status)
    },
    onSuccess: () => {
      notifyOk('Uploaded')
      void qc.invalidateQueries({ queryKey: ['objects', bucket] })
      void qc.invalidateQueries({ queryKey: ['buckets'] })
    },
    onError: notifyErr,
  })
  const remove = useMutation({
    mutationFn: async (name: string) => {
      const r = await admin.DELETE('/objects/{bucket}/object', { params: { path: { bucket }, query: { name } } })
      if (r.error) unwrap(r)
    },
    onSuccess: () => {
      notifyOk('Object deleted')
      setDeleting(null)
      void qc.invalidateQueries({ queryKey: ['objects', bucket] })
    },
    onError: notifyErr,
  })
  const rows = objs.data ?? []
  return (
    <>
      <Group mb="xs" justify="space-between">
        <Text size="sm" c="dimmed">
          {managed ? 'Managed by pulsed (event payloads too large for NATS); read-only here.' : 'Objects up to 64 MiB can be uploaded here.'}
        </Text>
        {!managed && (
          <Can perm="streams.write">
          <FileButton onChange={(f) => f && upload.mutate(f)}>
            {(props) => (
              <Button {...props} leftSection={<IconUpload size={14} />} loading={upload.isPending}>
                Upload
              </Button>
            )}
          </FileButton>
          </Can>
        )}
      </Group>
      <ErrorAlert error={objs.error} />
      <Paper withBorder>
        <Table data-testid="objects" verticalSpacing={6} highlightOnHover>
          <Table.Thead>
            <Table.Tr>
              <Table.Th>Name</Table.Th>
              <Table.Th ta="right">Size</Table.Th>
              <Table.Th>Modified</Table.Th>
              <Table.Th>Digest</Table.Th>
              <Table.Th />
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {rows.length === 0 && (
              <Table.Tr>
                <Table.Td colSpan={5}>
                  <Text size="sm" c="dimmed">
                    {objs.isLoading ? 'Loading…' : 'No objects'}
                  </Text>
                </Table.Td>
              </Table.Tr>
            )}
            {rows.map((o) => (
              <Table.Tr key={o.name}>
                <Table.Td>
                  <Text size="sm" ff="monospace">
                    {o.name}
                  </Text>
                </Table.Td>
                <Table.Td ta="right">{formatBytes(o.size)}</Table.Td>
                <Table.Td>
                  <Text size="sm" ff="monospace">
                    {formatTime(o.modified)}
                  </Text>
                </Table.Td>
                <Table.Td>
                  <Tooltip label={o.digest}>
                    <Text size="xs" ff="monospace" c="dimmed">
                      {o.digest.slice(0, 20)}…
                    </Text>
                  </Tooltip>
                </Table.Td>
                <Table.Td ta="right">
                  <Group gap={4} justify="flex-end" wrap="nowrap">
                    <ActionIcon
                      component="a"
                      variant="subtle"
                      color="gray"
                      aria-label={`Download ${o.name}`}
                      href={`/api/admin/v1/objects/${encodeURIComponent(bucket)}/download?name=${encodeURIComponent(o.name)}`}
                    >
                      <IconDownload size={16} />
                    </ActionIcon>
                    {!managed && (
                      <Can perm="streams.write">
                        <ActionIcon variant="subtle" color="red" aria-label={`Delete ${o.name}`} onClick={() => setDeleting(o)}>
                          <IconTrash size={16} />
                        </ActionIcon>
                      </Can>
                    )}
                  </Group>
                </Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      </Paper>
      <ConfirmModal
        opened={!!deleting}
        title={`Delete ${deleting?.name ?? ''}`}
        confirmText={deleting?.name ?? ''}
        onClose={() => setDeleting(null)}
        onConfirm={() => {
          if (deleting) remove.mutate(deleting.name)
        }}
        loading={remove.isPending}
      >
        The object is removed from {bucket}.
      </ConfirmModal>
    </>
  )
}

function KVPage() {
  const search = Route.useSearch()
  const navigate = useNavigate({ from: Route.fullPath })
  const buckets = useBuckets()
  const qc = useQueryClient()
  const tab = search.tab ?? 'kv'
  const [creating, setCreating] = useState(false)
  const [dropping, setDropping] = useState(false)
  const list = tab === 'kv' ? (buckets.data?.kv ?? []) : (buckets.data?.objects ?? [])
  const bucket = search.bucket && list.some((b) => b.name === search.bucket) ? search.bucket : (list[0]?.name ?? null)
  const objMeta = buckets.data?.objects.find((o) => o.name === bucket)
  const managed = tab === 'objects' && !!objMeta?.managed
  const kvMeta = buckets.data?.kv.find((b) => b.name === bucket)
  const drop = useMutation({
    mutationFn: async () => {
      const r =
        tab === 'kv'
          ? await admin.DELETE('/kv/{bucket}', { params: { path: { bucket: bucket! }, query: { confirm: bucket! } } })
          : await admin.DELETE('/objects/{bucket}', { params: { path: { bucket: bucket! }, query: { confirm: bucket! } } })
      if (r.error) unwrap(r)
    },
    onSuccess: () => {
      notifyOk(`Bucket ${bucket} deleted`)
      setDropping(false)
      void navigate({ search: { tab: search.tab } })
      void qc.invalidateQueries({ queryKey: ['buckets'] })
    },
    onError: notifyErr,
  })
  return (
    <>
      <PageHeader
        title="KV & Objects"
        description="Key-value buckets and object stores in the APP account. Values and objects are what tools stored; changes are audited."
        actions={
          <Can perm="streams.write">
            <Button variant="default" leftSection={<IconPlus size={14} />} onClick={() => setCreating(true)}>
              {tab === 'kv' ? 'New KV bucket' : 'New object store'}
            </Button>
          </Can>
        }
      />
      <ErrorAlert error={buckets.error} />
      <Tabs value={tab} onChange={(v) => void navigate({ search: { tab: v === 'objects' ? 'objects' : undefined } })} mb="sm">
        <Tabs.List>
          <Tabs.Tab value="kv">Key-value ({buckets.data?.kv.length ?? 0})</Tabs.Tab>
          <Tabs.Tab value="objects">Objects ({buckets.data?.objects.length ?? 0})</Tabs.Tab>
        </Tabs.List>
      </Tabs>
      {list.length === 0 ? (
        <Text size="sm" c="dimmed">
          {buckets.isLoading ? 'Loading…' : tab === 'kv' ? 'No KV buckets yet.' : 'No object stores yet.'}
        </Text>
      ) : (
        <>
          <Group mb="sm" align="flex-end">
            <Select
              label="Bucket"
              w={260}
              data={list.map((b) => b.name)}
              value={bucket}
              onChange={(v) => void navigate({ search: { tab: search.tab, bucket: v ?? undefined } })}
              allowDeselect={false}
              searchable
            />
            {tab === 'kv' && kvMeta && (
              <Text size="sm" c="dimmed" pb={6}>
                {formatNumber(kvMeta.values)} values · {formatBytes(kvMeta.bytes)} · history {kvMeta.history}
                {kvMeta.ttl_seconds ? ` · TTL ${kvMeta.ttl_seconds}s` : ''}
              </Text>
            )}
            {tab === 'objects' && objMeta && (
              <Text size="sm" c="dimmed" pb={6}>
                {formatBytes(objMeta.size)}
                {objMeta.managed ? ' · ' : ''}
                {objMeta.managed && (
                  <Badge size="sm" variant="light">
                    managed
                  </Badge>
                )}
              </Text>
            )}
            {!managed && bucket && (
              <Can perm="streams.write">
                <Button variant="subtle" color="red" ml="auto" leftSection={<IconTrash size={14} />} onClick={() => setDropping(true)}>
                  Delete bucket
                </Button>
              </Can>
            )}
          </Group>
          {bucket && (tab === 'kv' ? <KVTab key={bucket} bucket={bucket} /> : <ObjectsTab key={bucket} bucket={bucket} managed={managed} />)}
        </>
      )}
      {creating && <CreateBucketModal kind={tab} onClose={() => setCreating(false)} />}
      <ConfirmModal
        opened={dropping}
        title={`Delete bucket ${bucket ?? ''}`}
        confirmText={bucket ?? ''}
        onClose={() => setDropping(false)}
        onConfirm={() => drop.mutate()}
        loading={drop.isPending}
      >
        Every value and its history in {bucket} is deleted. This cannot be undone (backups can restore it).
      </ConfirmModal>
    </>
  )
}
