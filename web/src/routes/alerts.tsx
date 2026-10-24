import { ActionIcon, Alert, Badge, Button, Card, Group, Modal, NumberInput, SegmentedControl, Select, SimpleGrid, Stack, Switch, Table, Text, TextInput, Title, Tooltip } from '@mantine/core'
import { useForm } from '@mantine/form'
import { IconAlertTriangle, IconBell, IconCircleCheck, IconInfoCircle, IconPencil, IconPlayerPlay, IconPlus, IconSend, IconTrash } from '@tabler/icons-react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { createFileRoute } from '@tanstack/react-router'
import { useMemo, useState, type ReactNode } from 'react'
import { admin, unwrap } from '@/api/client'
import { useAlertHistory, useAlerts } from '@/api/queries'
import { ConfirmModal } from '@/components/ConfirmModal'
import { ErrorAlert } from '@/components/ErrorAlert'
import { PageHeader } from '@/components/PageHeader'
import { Can } from '@/components/Can'
import { useCan } from '@/api/auth'
import { kindFields, paramSummary, severityColor, type AlertParams, type AlertRule } from '@/lib/alerts'
import { formatTime, timeAgo } from '@/lib/format'
import { notifyErr, notifyOk } from '@/lib/notify'

export const Route = createFileRoute('/alerts')({ component: Alerts })

const severityIcon: Record<string, ReactNode> = {
  critical: <IconAlertTriangle size={12} />,
  warning: <IconAlertTriangle size={12} />,
  info: <IconInfoCircle size={12} />,
}

function SeverityBadge({ severity }: { severity: string }) {
  return (
    <Badge color={severityColor[severity] ?? 'gray'} variant="light" leftSection={severityIcon[severity]}>
      {severity}
    </Badge>
  )
}

function Section({ title, children, action }: { title: string; children: ReactNode; action?: ReactNode }) {
  return (
    <Card mb="sm">
      <Group justify="space-between" mb="xs">
        <Title order={5}>{title}</Title>
        {action}
      </Group>
      {children}
    </Card>
  )
}

interface FormValues {
  name: string
  severity: string
  for_seconds: number
  enabled: boolean
  params: AlertParams
}

const emptyCustom: FormValues = {
  name: '',
  severity: 'warning',
  for_seconds: 0,
  enabled: true,
  params: { stream: 'EVENTS', dimension: 'prefix', key: '', metric: 'msgs', op: 'below', threshold: 1, window_minutes: 60 },
}

function RuleModal({ rule, opened, onClose }: { rule: AlertRule | null; opened: boolean; onClose: () => void }) {
  const qc = useQueryClient()
  const custom = !rule || !rule.builtin
  const form = useForm<FormValues>({
    initialValues: rule
      ? { name: rule.name, severity: rule.severity, for_seconds: rule.for_seconds, enabled: rule.enabled, params: rule.params }
      : emptyCustom,
    validate: { name: (v) => (custom && !v.trim() ? 'Required' : null) },
  })
  const save = useMutation({
    mutationFn: async (v: FormValues) => {
      const body = { name: custom ? v.name : undefined, severity: v.severity as 'info' | 'warning' | 'critical', enabled: v.enabled, for_seconds: v.for_seconds, params: v.params }
      return rule
        ? unwrap(await admin.PUT('/alerts/rules/{id}', { params: { path: { id: rule.id } }, body }))
        : unwrap(await admin.POST('/alerts/rules', { body }))
    },
    onSuccess: (r) => {
      notifyOk(rule ? `Rule ${r.name} saved` : `Rule ${r.name} created`)
      void qc.invalidateQueries({ queryKey: ['alerts'] })
      onClose()
    },
    onError: notifyErr,
  })
  const p = form.values.params
  const fields = rule?.builtin ? (kindFields[rule.kind] ?? []) : []
  return (
    <Modal opened={opened} onClose={onClose} title={rule ? `Edit ${rule.name}` : 'New threshold rule'} size="lg">
      <form onSubmit={form.onSubmit((v) => save.mutate(v))}>
        <Stack gap="sm">
          {rule?.builtin && (
            <Text size="sm" c="dimmed">
              {rule.description}
            </Text>
          )}
          {custom && <TextInput label="Name" required {...form.getInputProps('name')} />}
          <SimpleGrid cols={2} spacing="sm">
            <Select label="Severity" data={['info', 'warning', 'critical']} allowDeselect={false} {...form.getInputProps('severity')} />
            <NumberInput label="Hold for (seconds)" description="Condition must hold this long" min={0} max={604800} {...form.getInputProps('for_seconds')} />
          </SimpleGrid>
          {fields.length > 0 && (
            <SimpleGrid cols={2} spacing="sm">
              {fields.map((f) => (
                <NumberInput key={f.key} label={f.label} min={f.min} max={f.max} step={f.step} decimalScale={f.step ? 2 : 0} {...form.getInputProps(`params.${f.key}`)} />
              ))}
            </SimpleGrid>
          )}
          {custom && (
            <>
              <SimpleGrid cols={2} spacing="sm">
                <TextInput label="Stream" {...form.getInputProps('params.stream')} />
                <Select
                  label="Count"
                  data={[
                    { value: 'prefix', label: 'Subject prefix' },
                    { value: 'type', label: 'Event type' },
                    { value: 'total', label: 'All events' },
                  ]}
                  allowDeselect={false}
                  {...form.getInputProps('params.dimension')}
                />
              </SimpleGrid>
              {p.dimension !== 'total' && (
                <TextInput
                  label={p.dimension === 'type' ? 'ce-type' : 'Subject prefix'}
                  description={p.dimension === 'type' ? 'e.g. home.door.opened.v1 or (untyped)' : '1–4 tokens, e.g. evt.home (per minute) or evt.home.door (per hour)'}
                  {...form.getInputProps('params.key')}
                />
              )}
              <SimpleGrid cols={2} spacing="sm">
                <Stack gap={4}>
                  <Text size="sm" fw={500}>
                    Fire when
                  </Text>
                  <SegmentedControl
                    data={[
                      { value: 'below', label: 'Below' },
                      { value: 'above', label: 'Above' },
                    ]}
                    {...form.getInputProps('params.op')}
                  />
                </Stack>
                <Select
                  label="Metric"
                  data={[
                    { value: 'msgs', label: 'Events' },
                    { value: 'bytes', label: 'Payload bytes' },
                  ]}
                  allowDeselect={false}
                  {...form.getInputProps('params.metric')}
                />
                <NumberInput label="Threshold" min={0} {...form.getInputProps('params.threshold')} />
                <NumberInput label="Window (minutes)" min={1} max={43200} {...form.getInputProps('params.window_minutes')} />
              </SimpleGrid>
              <Text size="xs" c="dimmed">
                "Below 1 in 60 minutes" is a dead-man switch: it fires when a source goes quiet. Counts use completed rollup buckets.
              </Text>
            </>
          )}
          <Switch label="Enabled" {...form.getInputProps('enabled', { type: 'checkbox' })} />
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" loading={save.isPending}>
              {rule ? 'Save' : 'Create'}
            </Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  )
}

function Alerts() {
  const canWrite = useCan('alerts.write')
  const alerts = useAlerts()
  const history = useAlertHistory()
  const qc = useQueryClient()
  const [editing, setEditing] = useState<AlertRule | null>(null)
  const [creating, setCreating] = useState(false)
  const [deleting, setDeleting] = useState<AlertRule | null>(null)
  const refresh = () => qc.invalidateQueries({ queryKey: ['alerts'] })
  const evaluate = useMutation({
    mutationFn: async () => unwrap(await admin.POST('/alerts/evaluate')),
    onSuccess: (o) => {
      qc.setQueryData(['alerts'], o)
      void qc.invalidateQueries({ queryKey: ['alerts', 'history'] })
      notifyOk(`Evaluated: ${o.firing} firing`)
    },
    onError: notifyErr,
  })
  const toggle = useMutation({
    mutationFn: async (r: AlertRule) =>
      unwrap(
        await admin.PUT('/alerts/rules/{id}', {
          params: { path: { id: r.id } },
          body: { enabled: !r.enabled, severity: r.severity, params: r.params, for_seconds: r.for_seconds },
        }),
      ),
    onSuccess: (r) => {
      notifyOk(`${r.name} ${r.enabled ? 'enabled' : 'disabled'}`)
      void refresh()
    },
    onError: notifyErr,
  })
  const remove = useMutation({
    mutationFn: async (r: AlertRule) => {
      const res = await admin.DELETE('/alerts/rules/{id}', { params: { path: { id: r.id } } })
      if (res.error) unwrap(res)
    },
    onSuccess: () => {
      notifyOk('Rule deleted')
      setDeleting(null)
      void refresh()
    },
    onError: notifyErr,
  })
  const test = useMutation({
    mutationFn: async (name: string) => unwrap(await admin.POST('/alerts/notifiers/{name}/test', { params: { path: { name } } })),
    onSuccess: (s) => {
      notifyOk(`Test sent to ${s.name}`)
      void refresh()
    },
    onError: notifyErr,
  })
  const rules = alerts.data?.rules ?? []
  const firing = useMemo(() => rules.flatMap((r) => r.instances.filter((i) => i.state === 'firing').map((i) => ({ rule: r, inst: i }))), [rules])
  const hist = history.data?.pages.flat() ?? []
  const notifiers = alerts.data?.notifiers ?? []

  return (
    <>
      <PageHeader
        title="Alerts"
        description="Built-in and custom rules, evaluated every minute. Transitions are stored, published as _sys.alert.<rule> events and sent to the notifiers."
        actions={
          <Can perm="alerts.write">
            <Button variant="default" leftSection={<IconPlayerPlay size={14} />} loading={evaluate.isPending} onClick={() => evaluate.mutate()}>
              Evaluate now
            </Button>
            <Button leftSection={<IconPlus size={14} />} onClick={() => setCreating(true)}>
              New rule
            </Button>
          </Can>
        }
      />
      <ErrorAlert error={alerts.error} />

      <Section title={`Firing now (${firing.length})`}>
        {firing.length === 0 ? (
          <Group gap={6} data-testid="no-firing">
            <IconCircleCheck size={16} color="var(--mantine-color-teal-6)" />
            <Text size="sm" c="dimmed">
              Nothing is firing.
            </Text>
          </Group>
        ) : (
          <Table data-testid="firing" verticalSpacing={6}>
            <Table.Tbody>
              {firing.map(({ rule, inst }) => (
                <Table.Tr key={`${rule.id}:${inst.key}`}>
                  <Table.Td w={110}>
                    <SeverityBadge severity={rule.severity} />
                  </Table.Td>
                  <Table.Td>
                    <Text size="sm" fw={500}>
                      {rule.name}
                      {inst.key ? ` · ${inst.key}` : ''}
                    </Text>
                    <Text size="xs" c="dimmed">
                      {inst.summary}
                    </Text>
                  </Table.Td>
                  <Table.Td w={140}>
                    <Text size="xs" c="dimmed">
                      since {timeAgo(inst.fired_at ?? inst.since)}
                    </Text>
                  </Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        )}
      </Section>

      <Section title="Notifiers">
        {notifiers.length === 0 ? (
          <Alert color="gray" icon={<IconBell size={16} />} title="No notifiers configured">
            Alerts are still recorded and published as events. Set PULSE_TELEGRAM_BOT_TOKEN_FILE + PULSE_TELEGRAM_CHAT_ID or PULSE_DISCORD_WEBHOOK_URL_FILE to
            get messages.
          </Alert>
        ) : (
          <Table data-testid="notifiers" verticalSpacing={6}>
            <Table.Thead>
              <Table.Tr>
                <Table.Th>Notifier</Table.Th>
                <Table.Th>Minimum severity</Table.Th>
                <Table.Th>Sent / failed / suppressed</Table.Th>
                <Table.Th>Last</Table.Th>
                <Table.Th />
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {notifiers.map((n) => (
                <Table.Tr key={n.name}>
                  <Table.Td>
                    <Text size="sm" fw={500}>
                      {n.name}
                    </Text>
                  </Table.Td>
                  <Table.Td>
                    <SeverityBadge severity={n.min_severity} />
                  </Table.Td>
                  <Table.Td>
                    <Text size="sm" ff="monospace">
                      {n.sent} / {n.failed} / {n.suppressed}
                    </Text>
                  </Table.Td>
                  <Table.Td>
                    {n.last_error ? (
                      <Text size="xs" c="red">
                        {n.last_error}
                      </Text>
                    ) : (
                      <Text size="xs" c="dimmed">
                        {n.last_sent ? `sent ${timeAgo(n.last_sent)}` : 'nothing sent yet'}
                      </Text>
                    )}
                  </Table.Td>
                  <Table.Td ta="right">
                    <Can perm="alerts.write">
                      <Button size="compact-sm" variant="default" leftSection={<IconSend size={12} />} loading={test.isPending && test.variables === n.name} onClick={() => test.mutate(n.name)}>
                        Test
                      </Button>
                    </Can>
                  </Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        )}
      </Section>

      <Section title="Rules">
        <Table.ScrollContainer minWidth={820}>
          <Table data-testid="rules" verticalSpacing={6}>
            <Table.Thead>
              <Table.Tr>
                <Table.Th>Rule</Table.Th>
                <Table.Th>Severity</Table.Th>
                <Table.Th>Condition</Table.Th>
                <Table.Th>State</Table.Th>
                <Table.Th>Enabled</Table.Th>
                <Table.Th />
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {rules.map((r) => {
                const nFiring = r.instances.filter((i) => i.state === 'firing').length
                const nPending = r.instances.length - nFiring
                return (
                  <Table.Tr key={r.id} data-testid={`rule-${r.id}`}>
                    <Table.Td maw={360}>
                      <Group gap={6}>
                        <Text size="sm" fw={500}>
                          {r.name}
                        </Text>
                        {!r.builtin && (
                          <Badge size="xs" variant="outline" color="gray">
                            custom
                          </Badge>
                        )}
                      </Group>
                      <Text size="xs" c="dimmed" lineClamp={2}>
                        {r.description}
                      </Text>
                    </Table.Td>
                    <Table.Td>
                      <SeverityBadge severity={r.severity} />
                    </Table.Td>
                    <Table.Td>
                      <Text size="sm" ff="monospace">
                        {paramSummary(r.kind, r.params)}
                        {r.for_seconds ? ` for ${r.for_seconds}s` : ''}
                      </Text>
                    </Table.Td>
                    <Table.Td>
                      {!r.enabled ? (
                        <Badge variant="dot" color="gray">
                          disabled
                        </Badge>
                      ) : !r.last_eval ? (
                        <Badge variant="dot" color="gray">
                          not evaluated
                        </Badge>
                      ) : r.last_error ? (
                        <Tooltip label={r.last_error} multiline maw={360}>
                          <Badge variant="dot" color="red">
                            evaluation error
                          </Badge>
                        </Tooltip>
                      ) : r.no_data ? (
                        <Tooltip label={r.no_data}>
                          <Badge variant="dot" color="gray">
                            no data
                          </Badge>
                        </Tooltip>
                      ) : nFiring > 0 ? (
                        <Badge variant="dot" color={severityColor[r.severity]}>
                          {nFiring} firing
                        </Badge>
                      ) : nPending > 0 ? (
                        <Badge variant="dot" color="yellow">
                          {nPending} pending
                        </Badge>
                      ) : (
                        <Badge variant="dot" color="teal">
                          ok
                        </Badge>
                      )}
                    </Table.Td>
                    <Table.Td>
                      <Switch aria-label={`Enable ${r.name}`} checked={r.enabled} onChange={() => toggle.mutate(r)} disabled={toggle.isPending || !canWrite} />
                    </Table.Td>
                    <Table.Td ta="right">
                      <Group gap={4} justify="flex-end" wrap="nowrap">
                        <Can perm="alerts.write">
                        <ActionIcon variant="subtle" color="gray" aria-label={`Edit ${r.name}`} onClick={() => setEditing(r)}>
                          <IconPencil size={16} />
                        </ActionIcon>
                        {!r.builtin && (
                          <ActionIcon variant="subtle" color="red" aria-label={`Delete ${r.name}`} onClick={() => setDeleting(r)}>
                            <IconTrash size={16} />
                          </ActionIcon>
                        )}
                        </Can>
                      </Group>
                    </Table.Td>
                  </Table.Tr>
                )
              })}
            </Table.Tbody>
          </Table>
        </Table.ScrollContainer>
      </Section>

      <Section title="History">
        <Table data-testid="alert-history" verticalSpacing={4}>
          <Table.Thead>
            <Table.Tr>
              <Table.Th>Time</Table.Th>
              <Table.Th>State</Table.Th>
              <Table.Th>Rule</Table.Th>
              <Table.Th>Summary</Table.Th>
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {hist.length === 0 && (
              <Table.Tr>
                <Table.Td colSpan={4}>
                  <Text size="sm" c="dimmed">
                    No alert has fired yet.
                  </Text>
                </Table.Td>
              </Table.Tr>
            )}
            {hist.map((h) => (
              <Table.Tr key={h.id}>
                <Table.Td w={200}>
                  <Text size="xs" ff="monospace">
                    {formatTime(h.time)}
                  </Text>
                </Table.Td>
                <Table.Td w={110}>
                  {h.state === 'firing' ? (
                    <SeverityBadge severity={h.severity} />
                  ) : (
                    <Badge color="teal" variant="light" leftSection={<IconCircleCheck size={12} />}>
                      resolved
                    </Badge>
                  )}
                </Table.Td>
                <Table.Td>
                  <Text size="sm">
                    {h.rule_name}
                    {h.key ? ` · ${h.key}` : ''}
                  </Text>
                </Table.Td>
                <Table.Td>
                  <Text size="xs" c="dimmed">
                    {h.summary}
                  </Text>
                </Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
        {history.hasNextPage && (
          <Group justify="center" mt="xs">
            <Button size="xs" variant="default" loading={history.isFetchingNextPage} onClick={() => void history.fetchNextPage()}>
              Load more
            </Button>
          </Group>
        )}
      </Section>

      {(creating || editing) && (
        <RuleModal
          key={editing?.id ?? 'new'}
          rule={editing}
          opened
          onClose={() => {
            setCreating(false)
            setEditing(null)
          }}
        />
      )}
      <ConfirmModal
        opened={!!deleting}
        title={`Delete rule ${deleting?.name ?? ''}`}
        confirmText={deleting?.name ?? ''}
        onClose={() => setDeleting(null)}
        onConfirm={() => {
          if (deleting) remove.mutate(deleting)
        }}
        loading={remove.isPending}
      >
        The rule and its live state are removed. History entries stay.
      </ConfirmModal>
    </>
  )
}
