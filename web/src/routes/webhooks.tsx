import { ActionIcon, Badge, Button, CopyButton, Drawer, Group, Modal, Select, Stack, Switch, Table, Tabs, TagsInput, Text, TextInput, Tooltip } from '@mantine/core'
import { useForm } from '@mantine/form'
import {
  IconCheck,
  IconCopy,
  IconEdit,
  IconHistory,
  IconKey,
  IconPlayerPause,
  IconPlayerPlay,
  IconPlus,
  IconRefresh,
  IconSend,
  IconTrash,
} from '@tabler/icons-react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { createFileRoute } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { admin, unwrap, type Delivery, type InboundHook, type OutboundHook } from '@/api/client'
import { useDeliveries, useInboundHooks, useOutboundHooks } from '@/api/queries'
import { ConfirmModal } from '@/components/ConfirmModal'
import { ErrorAlert } from '@/components/ErrorAlert'
import { PageHeader } from '@/components/PageHeader'
import { Can } from '@/components/Can'
import { SecretModal } from '@/components/SecretModal'
import { formatNumber, formatTime, timeAgo } from '@/lib/format'
import { notifyErr, notifyOk } from '@/lib/notify'

export const Route = createFileRoute('/webhooks')({ component: Webhooks })

type Kind = InboundHook['kind']

const kindHelp: Record<Kind, string> = {
  github: 'Paste the secret into the GitHub webhook "Secret" field; content type application/json.',
  stripe: 'Use the endpoint secret Stripe shows (whsec_…) as the secret here.',
  hmac: 'The sender signs the raw body with HMAC-SHA256 using this secret.',
  secret: 'The sender sends this secret in a header; use only when the sender cannot sign.',
}

function Copyable({ value }: { value: string }) {
  return (
    <Group gap={4} wrap="nowrap">
      <Text size="sm" ff="monospace" truncate maw={320}>
        {value}
      </Text>
      <CopyButton value={value}>
        {({ copied, copy }) => (
          <ActionIcon onClick={copy} aria-label="Copy URL" color={copied ? 'teal' : 'gray'}>
            {copied ? <IconCheck size={14} /> : <IconCopy size={14} />}
          </ActionIcon>
        )}
      </CopyButton>
    </Group>
  )
}

interface InboundValues {
  name: string
  kind: Kind
  subject: string
  type: string
  header: string
  encoding: string
  prefix: string
  timestamp_header: string
  secret: string
  enabled: boolean
}

function InboundForm({ hook, opened, onClose, onSecret }: { hook: InboundHook | null; opened: boolean; onClose: () => void; onSecret: (s: string, kind: Kind) => void }) {
  const qc = useQueryClient()
  const form = useForm<InboundValues>({
    initialValues: { name: '', kind: 'github', subject: '', type: '', header: '', encoding: 'hex', prefix: '', timestamp_header: '', secret: '', enabled: true },
    validate: { name: (v) => (/^[a-z0-9][a-z0-9_-]{0,47}$/.test(v) ? null : 'lowercase letters, digits, _ and -') },
  })
  useEffect(() => {
    if (!opened) return
    if (hook) {
      const c = hook.config ?? {}
      form.setValues({
        name: hook.name,
        kind: hook.kind,
        subject: hook.subject,
        type: c.type ?? '',
        header: c.header ?? '',
        encoding: c.encoding || 'hex',
        prefix: c.prefix ?? '',
        timestamp_header: c.timestamp_header ?? '',
        secret: '',
        enabled: hook.enabled,
      })
    } else form.reset()
  }, [opened, hook]) // eslint-disable-line react-hooks/exhaustive-deps
  const save = useMutation({
    mutationFn: async (v: InboundValues) => {
      const body = {
        name: v.name,
        kind: v.kind,
        subject: v.subject || undefined,
        enabled: v.enabled,
        secret: v.secret || undefined,
        config: {
          type: v.type || undefined,
          header: v.header || undefined,
          encoding: v.kind === 'hmac' ? (v.encoding as 'hex' | 'base64') : undefined,
          prefix: v.prefix || undefined,
          timestamp_header: v.timestamp_header || undefined,
        },
      }
      return hook
        ? unwrap(await admin.PUT('/hooks/inbound/{id}', { params: { path: { id: hook.id } }, body }))
        : unwrap(await admin.POST('/hooks/inbound', { body }))
    },
    onSuccess: (r, v) => {
      void qc.invalidateQueries({ queryKey: ['hooks', 'in'] })
      onClose()
      if (r.secret) onSecret(r.secret, v.kind)
      else notifyOk('Hook saved')
    },
    onError: notifyErr,
  })
  const k = form.values.kind
  return (
    <Modal opened={opened} onClose={onClose} title={hook ? `Edit ${hook.name}` : 'Create inbound hook'} size="lg">
      <form onSubmit={form.onSubmit((v) => save.mutate(v))}>
        <Stack gap="sm">
          <Group grow>
            <TextInput label="Name" required description="Part of the URL: /hooks/<name>" {...form.getInputProps('name')} />
            <Select
              label="Verifier"
              allowDeselect={false}
              data={[
                { value: 'github', label: 'GitHub (X-Hub-Signature-256)' },
                { value: 'stripe', label: 'Stripe (Stripe-Signature)' },
                { value: 'hmac', label: 'Generic HMAC-SHA256' },
                { value: 'secret', label: 'Shared secret header' },
              ]}
              {...form.getInputProps('kind')}
            />
          </Group>
          <Text size="xs" c="dimmed">
            {kindHelp[k]}
          </Text>
          <Group grow>
            <TextInput label="Subject" placeholder={`evt.hook.${form.values.name || '<name>'}.received`} {...form.getInputProps('subject')} />
            <TextInput label="Event type" placeholder={k === 'github' ? 'github.<event>' : k === 'stripe' ? 'stripe.<type>' : `hook.${form.values.name || '<name>'}`} {...form.getInputProps('type')} />
          </Group>
          {(k === 'hmac' || k === 'secret') && (
            <Group grow>
              <TextInput label="Header" placeholder={k === 'hmac' ? 'X-Signature' : 'X-Webhook-Secret'} {...form.getInputProps('header')} />
              {k === 'hmac' && <Select label="Encoding" data={['hex', 'base64']} allowDeselect={false} {...form.getInputProps('encoding')} />}
              {k === 'hmac' && <TextInput label="Prefix" placeholder="sha256=" {...form.getInputProps('prefix')} />}
            </Group>
          )}
          {k === 'hmac' && (
            <TextInput label="Timestamp header" description="Optional: signs '<timestamp>.<body>' and rejects stale deliveries" {...form.getInputProps('timestamp_header')} />
          )}
          <TextInput
            label="Secret"
            type="password"
            description={hook ? 'Leave empty to keep the current secret' : 'Leave empty to generate one (shown once)'}
            {...form.getInputProps('secret')}
          />
          <Switch label="Enabled" {...form.getInputProps('enabled', { type: 'checkbox' })} />
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" loading={save.isPending}>
              {hook ? 'Save' : 'Create'}
            </Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  )
}

function Inbound() {
  const hooks = useInboundHooks()
  const qc = useQueryClient()
  const [editing, setEditing] = useState<InboundHook | null | 'new'>(null)
  const [secret, setSecret] = useState<{ value: string; kind: Kind } | null>(null)
  const [deleting, setDeleting] = useState<InboundHook | null>(null)
  const rotate = useMutation({
    mutationFn: async (h: InboundHook) =>
      unwrap(
        await admin.PUT('/hooks/inbound/{id}', {
          params: { path: { id: h.id } },
          body: { name: h.name, kind: h.kind, subject: h.subject, config: h.config, enabled: h.enabled, rotate_secret: true },
        }),
      ),
    onSuccess: (r) => r.secret && setSecret({ value: r.secret, kind: r.hook.kind }),
    onError: notifyErr,
  })
  const del = useMutation({
    mutationFn: async (id: string) => {
      const r = await admin.DELETE('/hooks/inbound/{id}', { params: { path: { id } } })
      if (r.error) unwrap(r)
    },
    onSuccess: () => {
      setDeleting(null)
      void qc.invalidateQueries({ queryKey: ['hooks', 'in'] })
    },
    onError: notifyErr,
  })
  return (
    <>
      <Group justify="space-between" mb="sm">
        <Text size="sm" c="dimmed">
          Providers POST to these URLs; verified requests become events. Bad signatures get 401 and count toward bans.
        </Text>
        <Can perm="webhooks.write">
          <Button leftSection={<IconPlus size={14} />} onClick={() => setEditing('new')}>
            Create inbound hook
          </Button>
        </Can>
      </Group>
      <ErrorAlert error={hooks.error} />
      <Table.ScrollContainer minWidth={900}>
        <Table striped data-testid="inbound-table">
          <Table.Thead>
            <Table.Tr>
              <Table.Th>Name</Table.Th>
              <Table.Th>Verifier</Table.Th>
              <Table.Th>URL</Table.Th>
              <Table.Th>Subject</Table.Th>
              <Table.Th ta="right">Accepted</Table.Th>
              <Table.Th ta="right">Rejected</Table.Th>
              <Table.Th>Last delivery</Table.Th>
              <Table.Th />
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {hooks.data?.length === 0 && (
              <Table.Tr>
                <Table.Td colSpan={8}>
                  <Text size="sm" c="dimmed">
                    No inbound hooks yet.
                  </Text>
                </Table.Td>
              </Table.Tr>
            )}
            {(hooks.data ?? []).map((h) => (
              <Table.Tr key={h.id}>
                <Table.Td fw={500}>
                  {h.name}
                  {!h.enabled && (
                    <Badge size="xs" color="gray" ml={6}>
                      disabled
                    </Badge>
                  )}
                </Table.Td>
                <Table.Td>
                  <Badge variant="light">{h.kind}</Badge>
                </Table.Td>
                <Table.Td>
                  <Copyable value={h.url} />
                </Table.Td>
                <Table.Td ff="monospace">{h.subject}</Table.Td>
                <Table.Td ta="right">{formatNumber(h.received)}</Table.Td>
                <Table.Td ta="right">{h.rejected ? <Text c="orange" size="sm" span>{formatNumber(h.rejected)}</Text> : 0}</Table.Td>
                <Table.Td>{h.last_received_at ? timeAgo(h.last_received_at) : 'never'}</Table.Td>
                <Table.Td>
                  <Group gap={2} wrap="nowrap">
                    <Can perm="webhooks.write">
                    <Tooltip label="Edit">
                      <ActionIcon aria-label={`Edit ${h.name}`} onClick={() => setEditing(h)}>
                        <IconEdit size={14} />
                      </ActionIcon>
                    </Tooltip>
                    <Tooltip label="Rotate secret">
                      <ActionIcon aria-label={`Rotate secret of ${h.name}`} onClick={() => rotate.mutate(h)}>
                        <IconKey size={14} />
                      </ActionIcon>
                    </Tooltip>
                    <Tooltip label="Delete">
                      <ActionIcon color="red" aria-label={`Delete ${h.name}`} onClick={() => setDeleting(h)}>
                        <IconTrash size={14} />
                      </ActionIcon>
                    </Tooltip>
                    </Can>
                  </Group>
                </Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      </Table.ScrollContainer>
      <InboundForm
        hook={editing === 'new' ? null : editing}
        opened={editing !== null}
        onClose={() => setEditing(null)}
        onSecret={(value, kind) => setSecret({ value, kind })}
      />
      <SecretModal secret={secret?.value ?? null} title="Webhook secret" onClose={() => setSecret(null)}>
        {secret && (
          <Text size="sm" c="dimmed">
            {kindHelp[secret.kind]}
          </Text>
        )}
      </SecretModal>
      <ConfirmModal
        opened={deleting !== null}
        onClose={() => setDeleting(null)}
        title={`Delete ${deleting?.name ?? ''}`}
        confirmText={deleting?.name ?? ''}
        loading={del.isPending}
        onConfirm={() => deleting && del.mutate(deleting.id)}
      >
        <Text size="sm">Deliveries to this URL will get 404. Events already received stay in EVENTS.</Text>
      </ConfirmModal>
    </>
  )
}

interface OutboundValues {
  name: string
  filter: string
  url: string
  headers: string[]
  secret: string
}

function OutboundForm({ hook, opened, onClose, onSecret }: { hook: OutboundHook | null; opened: boolean; onClose: () => void; onSecret: (s: string) => void }) {
  const qc = useQueryClient()
  const form = useForm<OutboundValues>({
    initialValues: { name: '', filter: 'evt.>', url: 'https://', headers: [], secret: '' },
    validate: {
      name: (v) => (/^[a-z0-9][a-z0-9_-]{0,47}$/.test(v) ? null : 'lowercase letters, digits, _ and -'),
      url: (v) => (/^https?:\/\/[^/]+/.test(v) ? null : 'http(s) URL'),
      headers: (v) => (v.every((h) => /^[A-Za-z0-9-]+=.*$/.test(h)) ? null : 'Headers are Name=value'),
    },
  })
  useEffect(() => {
    if (!opened) return
    if (hook) form.setValues({ name: hook.name, filter: hook.filter, url: hook.url, headers: Object.entries(hook.headers).map(([k, v]) => `${k}=${v}`), secret: '' })
    else form.reset()
  }, [opened, hook]) // eslint-disable-line react-hooks/exhaustive-deps
  const save = useMutation({
    mutationFn: async (v: OutboundValues) => {
      const body = {
        name: v.name,
        filter: v.filter,
        url: v.url,
        secret: v.secret || undefined,
        headers: Object.fromEntries(v.headers.map((h) => [h.slice(0, h.indexOf('=')), h.slice(h.indexOf('=') + 1)])),
      }
      return hook
        ? unwrap(await admin.PUT('/hooks/outbound/{id}', { params: { path: { id: hook.id } }, body }))
        : unwrap(await admin.POST('/hooks/outbound', { body }))
    },
    onSuccess: (r) => {
      void qc.invalidateQueries({ queryKey: ['hooks', 'out'] })
      onClose()
      if (r.secret) onSecret(r.secret)
      else notifyOk('Hook saved')
    },
    onError: notifyErr,
  })
  return (
    <Modal opened={opened} onClose={onClose} title={hook ? `Edit ${hook.name}` : 'Create outbound hook'} size="lg">
      <form onSubmit={form.onSubmit((v) => save.mutate(v))}>
        <Stack gap="sm">
          <Group grow>
            <TextInput label="Name" required {...form.getInputProps('name')} />
            <TextInput label="Subject filter" required ff="monospace" {...form.getInputProps('filter')} />
          </Group>
          <TextInput label="URL" required description="Internal addresses are refused unless allowlisted with PULSE_OUTBOUND_ALLOW" {...form.getInputProps('url')} />
          <TagsInput label="Extra headers" placeholder="Authorization=Bearer …" {...form.getInputProps('headers')} />
          <TextInput
            label="Signing secret"
            type="password"
            description={hook ? 'Leave empty to keep the current secret' : 'Leave empty to generate one (shown once)'}
            {...form.getInputProps('secret')}
          />
          {!hook && (
            <Text size="xs" c="dimmed">
              Only events published after creation are delivered, one at a time and in order, retried up to 12 times over about 2.3 hours.
            </Text>
          )}
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" loading={save.isPending}>
              {hook ? 'Save' : 'Create'}
            </Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  )
}

const outcomeColor: Record<Delivery['outcome'], string> = { delivered: 'teal', retry: 'yellow', dead: 'red', manual: 'indigo', test: 'gray' }

function DeliveryLog({ hook, onClose }: { hook: OutboundHook | null; onClose: () => void }) {
  const log = useDeliveries(hook?.id ?? null)
  const qc = useQueryClient()
  const retry = useMutation({
    mutationFn: async (id: number) => unwrap(await admin.POST('/hooks/deliveries/{delivery}/retry', { params: { path: { delivery: id } } })),
    onSuccess: (d) => {
      notifyOk(d.status_code ? `Delivered again: HTTP ${d.status_code}` : `Retry failed: ${d.error}`)
      void qc.invalidateQueries({ queryKey: ['hooks', 'out'] })
    },
    onError: notifyErr,
  })
  return (
    <Drawer opened={hook !== null} onClose={onClose} position="right" size="xl" title={`Deliveries · ${hook?.name ?? ''}`}>
      <Table data-testid="deliveries-table">
        <Table.Thead>
          <Table.Tr>
            <Table.Th>Time</Table.Th>
            <Table.Th>Seq</Table.Th>
            <Table.Th>#</Table.Th>
            <Table.Th>Outcome</Table.Th>
            <Table.Th>Status</Table.Th>
            <Table.Th ta="right">ms</Table.Th>
            <Table.Th />
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {(log.data ?? []).length === 0 && (
            <Table.Tr>
              <Table.Td colSpan={7}>
                <Text size="sm" c="dimmed">
                  No deliveries yet.
                </Text>
              </Table.Td>
            </Table.Tr>
          )}
          {(log.data ?? []).map((d) => (
            <Table.Tr key={d.id}>
              <Table.Td ff="monospace">{formatTime(d.time)}</Table.Td>
              <Table.Td>{d.seq || '—'}</Table.Td>
              <Table.Td>{d.attempt}</Table.Td>
              <Table.Td>
                <Badge size="sm" variant="light" color={outcomeColor[d.outcome]}>
                  {d.outcome}
                </Badge>
              </Table.Td>
              <Table.Td>
                <Tooltip label={d.error || d.response_excerpt || '—'} multiline maw={420}>
                  <Text size="sm" truncate maw={200}>
                    {d.status_code || d.error || '—'}
                  </Text>
                </Tooltip>
              </Table.Td>
              <Table.Td ta="right">{d.latency_ms}</Table.Td>
              <Table.Td>
                {d.seq > 0 && (
                  <Can perm="webhooks.write">
                  <Tooltip label="Deliver this event again">
                    <ActionIcon aria-label={`Retry delivery ${d.id}`} onClick={() => retry.mutate(d.id)} loading={retry.isPending && retry.variables === d.id}>
                      <IconRefresh size={14} />
                    </ActionIcon>
                  </Tooltip>
                  </Can>
                )}
              </Table.Td>
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
    </Drawer>
  )
}

function Outbound() {
  const hooks = useOutboundHooks()
  const qc = useQueryClient()
  const [editing, setEditing] = useState<OutboundHook | null | 'new'>(null)
  const [secret, setSecret] = useState<string | null>(null)
  const [deleting, setDeleting] = useState<OutboundHook | null>(null)
  const [log, setLog] = useState<OutboundHook | null>(null)
  const refresh = () => qc.invalidateQueries({ queryKey: ['hooks', 'out'] })
  const action = useMutation({
    mutationFn: async ({ h, op }: { h: OutboundHook; op: 'pause' | 'resume' | 'test' }) => {
      if (op === 'test') {
        const d = unwrap(await admin.POST('/hooks/outbound/{id}/test', { params: { path: { id: h.id } } }))
        return d.status_code ? `Test delivered: HTTP ${d.status_code}` : `Test failed: ${d.error}`
      }
      unwrap(await admin.POST(op === 'pause' ? '/hooks/outbound/{id}/pause' : '/hooks/outbound/{id}/resume', { params: { path: { id: h.id } } }))
      return op === 'pause' ? 'Paused' : 'Resumed'
    },
    onSuccess: (msg) => {
      notifyOk(msg)
      void refresh()
    },
    onError: notifyErr,
  })
  const del = useMutation({
    mutationFn: async (id: string) => {
      const r = await admin.DELETE('/hooks/outbound/{id}', { params: { path: { id } } })
      if (r.error) unwrap(r)
    },
    onSuccess: () => {
      setDeleting(null)
      void refresh()
    },
    onError: notifyErr,
  })
  return (
    <>
      <Group justify="space-between" mb="sm">
        <Text size="sm" c="dimmed">
          Matching events are POSTed with a Pulse-Signature header (t=…,v1=hex HMAC-SHA256 of &quot;t.body&quot;).
        </Text>
        <Can perm="webhooks.write">
          <Button leftSection={<IconPlus size={14} />} onClick={() => setEditing('new')}>
            Create outbound hook
          </Button>
        </Can>
      </Group>
      <ErrorAlert error={hooks.error} />
      <Table.ScrollContainer minWidth={900}>
        <Table striped data-testid="outbound-table">
          <Table.Thead>
            <Table.Tr>
              <Table.Th>Name</Table.Th>
              <Table.Th>Filter</Table.Th>
              <Table.Th>URL</Table.Th>
              <Table.Th>State</Table.Th>
              <Table.Th ta="right">Pending</Table.Th>
              <Table.Th>Last delivery</Table.Th>
              <Table.Th />
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {hooks.data?.length === 0 && (
              <Table.Tr>
                <Table.Td colSpan={7}>
                  <Text size="sm" c="dimmed">
                    No outbound hooks yet.
                  </Text>
                </Table.Td>
              </Table.Tr>
            )}
            {(hooks.data ?? []).map((h) => (
              <Table.Tr key={h.id}>
                <Table.Td fw={500}>{h.name}</Table.Td>
                <Table.Td ff="monospace">{h.filter}</Table.Td>
                <Table.Td>
                  <Text size="sm" ff="monospace" truncate maw={260}>
                    {h.url}
                  </Text>
                </Table.Td>
                <Table.Td>
                  <Badge variant="light" color={h.paused ? 'yellow' : 'teal'}>
                    {h.paused ? 'paused' : 'active'}
                  </Badge>
                </Table.Td>
                <Table.Td ta="right">{formatNumber(h.pending)}</Table.Td>
                <Table.Td>
                  {h.last ? (
                    <Group gap={4} wrap="nowrap">
                      <Badge size="xs" variant="light" color={outcomeColor[h.last.outcome]}>
                        {h.last.outcome}
                      </Badge>
                      <Text size="sm">{timeAgo(h.last.time)}</Text>
                    </Group>
                  ) : (
                    'never'
                  )}
                </Table.Td>
                <Table.Td>
                  <Group gap={2} wrap="nowrap">
                    <Tooltip label="Delivery log">
                      <ActionIcon aria-label={`Deliveries of ${h.name}`} onClick={() => setLog(h)}>
                        <IconHistory size={14} />
                      </ActionIcon>
                    </Tooltip>
                    <Can perm="webhooks.write">
                    <Tooltip label="Send a test event">
                      <ActionIcon aria-label={`Test ${h.name}`} onClick={() => action.mutate({ h, op: 'test' })}>
                        <IconSend size={14} />
                      </ActionIcon>
                    </Tooltip>
                    <Tooltip label={h.paused ? 'Resume' : 'Pause'}>
                      <ActionIcon aria-label={`${h.paused ? 'Resume' : 'Pause'} ${h.name}`} onClick={() => action.mutate({ h, op: h.paused ? 'resume' : 'pause' })}>
                        {h.paused ? <IconPlayerPlay size={14} /> : <IconPlayerPause size={14} />}
                      </ActionIcon>
                    </Tooltip>
                    <Tooltip label="Edit">
                      <ActionIcon aria-label={`Edit ${h.name}`} onClick={() => setEditing(h)}>
                        <IconEdit size={14} />
                      </ActionIcon>
                    </Tooltip>
                    <Tooltip label="Delete">
                      <ActionIcon color="red" aria-label={`Delete ${h.name}`} onClick={() => setDeleting(h)}>
                        <IconTrash size={14} />
                      </ActionIcon>
                    </Tooltip>
                    </Can>
                  </Group>
                </Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      </Table.ScrollContainer>
      <OutboundForm hook={editing === 'new' ? null : editing} opened={editing !== null} onClose={() => setEditing(null)} onSecret={setSecret} />
      <SecretModal secret={secret} title="Signing secret" onClose={() => setSecret(null)}>
        <Text size="sm" c="dimmed">
          Verify Pulse-Signature by computing HMAC-SHA256(secret, &quot;&lt;t&gt;.&lt;raw body&gt;&quot;) and comparing with v1.
        </Text>
      </SecretModal>
      <DeliveryLog hook={log} onClose={() => setLog(null)} />
      <ConfirmModal
        opened={deleting !== null}
        onClose={() => setDeleting(null)}
        title={`Delete ${deleting?.name ?? ''}`}
        confirmText={deleting?.name ?? ''}
        loading={del.isPending}
        onConfirm={() => deleting && del.mutate(deleting.id)}
      >
        <Text size="sm">Stops deliveries and removes the delivery log.</Text>
      </ConfirmModal>
    </>
  )
}

function Webhooks() {
  return (
    <>
      <PageHeader title="Webhooks" description="Receive events from other services, and push events to them." />
      <Tabs defaultValue="in" keepMounted={false}>
        <Tabs.List mb="sm">
          <Tabs.Tab value="in">Inbound</Tabs.Tab>
          <Tabs.Tab value="out">Outbound</Tabs.Tab>
        </Tabs.List>
        <Tabs.Panel value="in">
          <Inbound />
        </Tabs.Panel>
        <Tabs.Panel value="out">
          <Outbound />
        </Tabs.Panel>
      </Tabs>
    </>
  )
}
