import { Alert, Badge, Button, Card, Checkbox, Code, Drawer, Group, Modal, SimpleGrid, Stack, Switch, Table, Text, Textarea, TextInput, Title, Tooltip } from '@mantine/core'
import { IconAlertTriangle, IconCircleCheck, IconDatabaseExport, IconHistory, IconRestore, IconScissors } from '@tabler/icons-react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { createFileRoute } from '@tanstack/react-router'
import { useState, type ReactNode } from 'react'
import { admin, unwrap, type Schemas } from '@/api/client'
import { useBackups } from '@/api/queries'
import { ErrorAlert } from '@/components/ErrorAlert'
import { PageHeader } from '@/components/PageHeader'
import { Can } from '@/components/Can'
import { formatBytes, formatDuration, formatNumber, formatTime, timeAgo } from '@/lib/format'
import { notifyErr, notifyOk } from '@/lib/notify'

export const Route = createFileRoute('/backups')({ component: Backups })

type BackupRecord = Schemas['BackupRecord']
type StreamPlan = Schemas['StreamPlan']

const statusColor: Record<string, string> = { ok: 'teal', failed: 'red', running: 'blue' }

function StatusBadge({ status }: { status: string }) {
  return (
    <Badge variant="light" color={statusColor[status] ?? 'gray'} leftSection={status === 'failed' ? <IconAlertTriangle size={12} /> : undefined}>
      {status}
    </Badge>
  )
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <Group justify="space-between" wrap="nowrap">
      <Text size="sm" c="dimmed">
        {label}
      </Text>
      <Text size="sm" component="div" ta="right">
        {children}
      </Text>
    </Group>
  )
}

function RestoreModal({ backup, onClose }: { backup: BackupRecord; onClose: () => void }) {
  const qc = useQueryClient()
  const [identity, setIdentity] = useState('')
  const [plan, setPlan] = useState<Schemas['BackupPlan'] | null>(null)
  const [selected, setSelected] = useState<string[]>([])
  const [replace, setReplace] = useState(false)
  const [confirm, setConfirm] = useState('')
  const body = () => ({ identity: identity.trim(), streams: selected, replace })
  const doPlan = useMutation({
    mutationFn: async () => unwrap(await admin.POST('/backups/{id}/plan', { params: { path: { id: backup.id } }, body: { identity: identity.trim() } })),
    onSuccess: (p) => {
      setPlan(p)
      setSelected(p.streams.filter((s) => !s.exists).map((s) => s.name))
    },
    onError: notifyErr,
  })
  const restore = useMutation({
    mutationFn: async () =>
      unwrap(
        await admin.POST('/backups/{id}/restore', {
          params: { path: { id: backup.id }, query: replace ? { confirm } : {} },
          body: body(),
        }),
      ),
    onSuccess: () => {
      notifyOk('Restore started')
      void qc.invalidateQueries({ queryKey: ['backups'] })
      onClose()
    },
    onError: notifyErr,
  })
  const blocked = plan?.streams.some((s) => selected.includes(s.name) && s.exists) && !replace
  return (
    <Modal opened onClose={onClose} title={`Restore from ${backup.id}`} size="lg">
      <Stack gap="sm">
        <Textarea
          label="age identity"
          description="AGE-SECRET-KEY-1… (or the whole identity file). Used for this request only; never stored or logged."
          autosize
          minRows={2}
          styles={{ input: { fontFamily: 'var(--mantine-font-family-monospace)' } }}
          value={identity}
          onChange={(e) => setIdentity(e.currentTarget.value)}
          data-testid="identity"
        />
        {!plan ? (
          <Group justify="flex-end">
            <Button onClick={() => doPlan.mutate()} loading={doPlan.isPending} disabled={identity.trim().length < 10}>
              Show contents
            </Button>
          </Group>
        ) : (
          <>
            <Text size="xs" c="dimmed">
              Created {formatTime(plan.created)} by pulsed {plan.pulsed_version}. Files ({plan.files.map((f) => f.name).join(', ')}) are restored offline with{' '}
              <Code>pulsed backup restore --files</Code>.
            </Text>
            <Table data-testid="restore-plan" verticalSpacing={4}>
              <Table.Thead>
                <Table.Tr>
                  <Table.Th />
                  <Table.Th>Stream</Table.Th>
                  <Table.Th ta="right">Messages</Table.Th>
                  <Table.Th>Sequences</Table.Th>
                  <Table.Th>Now</Table.Th>
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {plan.streams.map((s: StreamPlan) => (
                  <Table.Tr key={s.name}>
                    <Table.Td w={36}>
                      <Checkbox
                        aria-label={`Restore ${s.name}`}
                        checked={selected.includes(s.name)}
                        onChange={(e) => setSelected((cur) => (e.currentTarget.checked ? [...cur, s.name] : cur.filter((n) => n !== s.name)))}
                      />
                    </Table.Td>
                    <Table.Td>
                      <Text size="sm" ff="monospace">
                        {s.name}
                      </Text>
                    </Table.Td>
                    <Table.Td ta="right">{formatNumber(s.msgs)}</Table.Td>
                    <Table.Td>
                      <Text size="xs" ff="monospace">
                        {s.first_seq}–{s.last_seq}
                      </Text>
                    </Table.Td>
                    <Table.Td>
                      {s.exists ? (
                        <Badge color="orange" variant="light">
                          exists
                        </Badge>
                      ) : (
                        <Badge color="gray" variant="light">
                          missing
                        </Badge>
                      )}
                    </Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
            <Switch
              label="Replace existing streams (deletes them first; events published since the backup are lost)"
              checked={replace}
              onChange={(e) => setReplace(e.currentTarget.checked)}
            />
            {replace && <TextInput label={`Type ${backup.id} to confirm`} aria-label="confirmation" value={confirm} onChange={(e) => setConfirm(e.currentTarget.value)} />}
            {blocked && (
              <Text size="xs" c="orange">
                A selected stream exists; enable replace or deselect it.
              </Text>
            )}
            <Group justify="flex-end">
              <Button variant="default" onClick={onClose}>
                Cancel
              </Button>
              <Button
                color={replace ? 'red' : undefined}
                leftSection={<IconRestore size={14} />}
                disabled={selected.length === 0 || blocked || (replace && confirm !== backup.id)}
                loading={restore.isPending}
                onClick={() => restore.mutate()}
              >
                Restore {selected.length} stream{selected.length === 1 ? '' : 's'}
              </Button>
            </Group>
          </>
        )}
      </Stack>
    </Modal>
  )
}

function Backups() {
  const q = useBackups()
  const qc = useQueryClient()
  const [detail, setDetail] = useState<BackupRecord | null>(null)
  const [restoring, setRestoring] = useState<BackupRecord | null>(null)
  const run = useMutation({
    mutationFn: async () => {
      const r = await admin.POST('/backups/run')
      if (r.error) unwrap(r)
    },
    onSuccess: () => {
      notifyOk('Backup started')
      void qc.invalidateQueries({ queryKey: ['backups'] })
    },
    onError: notifyErr,
  })
  const prune = useMutation({
    mutationFn: async () => {
      const r = await admin.POST('/backups/prune')
      if (r.error) unwrap(r)
    },
    onSuccess: () => {
      notifyOk('Retention applied')
      void qc.invalidateQueries({ queryKey: ['backups'] })
    },
    onError: notifyErr,
  })
  const d = q.data
  const st = d?.settings
  const lr = d?.last_restore
  return (
    <>
      <PageHeader
        title="Backups"
        description="Nightly, age-encrypted backups to S3: the SQLite state, keys.enc and every stream (NATSARC1 records, deduplicated). Every backup restores on its own."
        actions={
          d?.enabled && (
            <Can perm="backups.run">
              <Button variant="default" leftSection={<IconScissors size={14} />} loading={prune.isPending} disabled={!!st?.running} onClick={() => prune.mutate()}>
                Apply retention
              </Button>
              <Button leftSection={<IconDatabaseExport size={14} />} loading={run.isPending} disabled={!!st?.running} onClick={() => run.mutate()}>
                Back up now
              </Button>
            </Can>
          )
        }
      />
      <ErrorAlert error={q.error} />
      {d && !d.enabled && (
        <Alert color="gray" icon={<IconHistory size={16} />} title="Backups are not configured">
          Set PULSE_BACKUP_ENABLED, PULSE_BACKUP_S3_* and PULSE_BACKUP_AGE_RECIPIENTS (docs/operator/configuration.md). Create an identity with{' '}
          <Code>pulsed backup keygen</Code> and keep it off the server.
        </Alert>
      )}
      {d?.enabled && st && (
        <SimpleGrid cols={{ base: 1, md: 2 }} mb="sm">
          <Card>
            <Title order={5} mb="xs">
              Schedule
            </Title>
            <Stack gap={6}>
              <Row label="Daily at">{st.at} UTC</Row>
              <Row label="Next run">{formatTime(st.next_run)}</Row>
              <Row label="Retention">
                {st.keep.daily} daily · {st.keep.weekly} weekly · {st.keep.monthly} monthly
              </Row>
              <Row label="Now">
                {st.running ? (
                  <Badge color="blue" variant="dot" data-testid="backup-running">
                    {st.running} running
                  </Badge>
                ) : (
                  <Badge color="gray" variant="dot">
                    idle
                  </Badge>
                )}
              </Row>
            </Stack>
          </Card>
          <Card>
            <Title order={5} mb="xs">
              Destination
            </Title>
            <Stack gap={6}>
              <Row label="S3 endpoint">
                <Code>{d.store.endpoint}</Code>
              </Row>
              <Row label="Bucket / prefix">
                <Code>
                  {d.store.bucket}/{d.store.prefix}
                </Code>
              </Row>
              <Row label="Encrypted to">
                {st.recipients.map((r) => (
                  <Tooltip key={r} label={r}>
                    <Code>{r.slice(0, 16)}…</Code>
                  </Tooltip>
                ))}
              </Row>
            </Stack>
          </Card>
        </SimpleGrid>
      )}
      {lr && (
        <Card mb="sm" data-testid="last-restore">
          <Group justify="space-between" mb="xs">
            <Title order={5}>Last restore: {lr.backup_id}</Title>
            <StatusBadge status={lr.status} />
          </Group>
          {lr.error && (
            <Text size="sm" c="red" mb="xs">
              {lr.error}
            </Text>
          )}
          <Text size="xs" c="dimmed">
            started {timeAgo(lr.started_at)}
            {lr.replace ? ' · replace' : ''}
          </Text>
          {lr.streams.map((s) => (
            <Text key={s.name} size="sm" ff="monospace">
              {s.name}: {s.result || s.action}
            </Text>
          ))}
        </Card>
      )}
      {d?.enabled && (
        <Card>
          <Title order={5} mb="xs">
            Catalog
          </Title>
          <Table.ScrollContainer minWidth={860}>
            <Table data-testid="backups-table" verticalSpacing={6} highlightOnHover>
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>Started</Table.Th>
                  <Table.Th>Status</Table.Th>
                  <Table.Th>Duration</Table.Th>
                  <Table.Th ta="right">Size</Table.Th>
                  <Table.Th ta="right">Uploaded</Table.Th>
                  <Table.Th ta="right">Chunks new / total</Table.Th>
                  <Table.Th>Verified</Table.Th>
                  <Table.Th />
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {d.records.length === 0 && (
                  <Table.Tr>
                    <Table.Td colSpan={8}>
                      <Text size="sm" c="dimmed">
                        No backup has run on this server yet.
                      </Text>
                    </Table.Td>
                  </Table.Tr>
                )}
                {d.records.map((r) => (
                  <Table.Tr key={r.id} style={{ cursor: 'pointer', opacity: r.pruned ? 0.55 : 1 }} onClick={() => setDetail(r)}>
                    <Table.Td>
                      <Text size="sm" ff="monospace">
                        {formatTime(r.started_at)}
                      </Text>
                      <Text size="xs" c="dimmed">
                        {r.trigger}
                        {r.pruned ? ' · pruned' : ''}
                      </Text>
                    </Table.Td>
                    <Table.Td>
                      <StatusBadge status={r.status} />
                    </Table.Td>
                    <Table.Td>{r.finished_at ? formatDuration(r.duration_ms / 1000) : '…'}</Table.Td>
                    <Table.Td ta="right">{formatBytes(r.logical_bytes)}</Table.Td>
                    <Table.Td ta="right">{formatBytes(r.uploaded_bytes)}</Table.Td>
                    <Table.Td ta="right">
                      <Text size="sm" ff="monospace">
                        {r.chunks_new} / {r.chunks_total}
                      </Text>
                    </Table.Td>
                    <Table.Td>
                      {r.status === 'ok' &&
                        (r.verified ? (
                          <Badge color="teal" variant="light" leftSection={<IconCircleCheck size={12} />}>
                            verified
                          </Badge>
                        ) : (
                          <Tooltip label={r.verify_error}>
                            <Badge color="red" variant="light">
                              missing objects
                            </Badge>
                          </Tooltip>
                        ))}
                    </Table.Td>
                    <Table.Td ta="right">
                      {r.status === 'ok' && !r.pruned && (
                        <Can perm="backups.restore">
                        <Button
                          size="compact-sm"
                          variant="default"
                          leftSection={<IconRestore size={12} />}
                          aria-label={`Restore ${r.id}`}
                          disabled={!!st?.running}
                          onClick={(e) => {
                            e.stopPropagation()
                            setRestoring(r)
                          }}
                        >
                          Restore…
                        </Button>
                        </Can>
                      )}
                    </Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        </Card>
      )}
      <Drawer opened={!!detail} onClose={() => setDetail(null)} title={detail?.id} position="right" size="md">
        {detail && (
          <Stack gap="xs">
            <StatusBadge status={detail.status} />
            {detail.error && (
              <Alert color="red" title="Failed">
                {detail.error}
              </Alert>
            )}
            <Row label="Started">{formatTime(detail.started_at)}</Row>
            <Row label="Finished">{detail.finished_at ? formatTime(detail.finished_at) : '—'}</Row>
            <Row label="Logical size">{formatBytes(detail.logical_bytes)}</Row>
            <Row label="Uploaded">{formatBytes(detail.uploaded_bytes)}</Row>
            <Title order={6} mt="sm">
              Streams
            </Title>
            <Table verticalSpacing={2}>
              <Table.Tbody>
                {detail.streams.map((s) => (
                  <Table.Tr key={s.name}>
                    <Table.Td>
                      <Text size="sm" ff="monospace">
                        {s.name}
                      </Text>
                    </Table.Td>
                    <Table.Td ta="right">{formatNumber(s.msgs)} msgs</Table.Td>
                    <Table.Td ta="right">{formatBytes(s.bytes)}</Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </Stack>
        )}
      </Drawer>
      {restoring && <RestoreModal backup={restoring} onClose={() => setRestoring(null)} />}
    </>
  )
}

