import { Alert, Badge, Button, Card, Group, Modal, NumberInput, SimpleGrid, Stack, Table, Text, Title } from '@mantine/core'
import { IconPlugConnectedX, IconRefresh, IconBan } from '@tabler/icons-react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { createFileRoute } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { admin, unwrap, type Grant } from '@/api/client'
import { useClient, useClientConnections, useSystemInfo } from '@/api/queries'
import { ConfirmModal } from '@/components/ConfirmModal'
import { ErrorAlert } from '@/components/ErrorAlert'
import { cleanGrants, GrantsEditor } from '@/components/GrantsEditor'
import { PageHeader } from '@/components/PageHeader'
import { Can } from '@/components/Can'
import { Snippets } from '@/components/Snippets'
import { formatBytes, formatGoDuration, formatNumber, formatTime, timeAgo } from '@/lib/format'
import { notifyErr, notifyOk } from '@/lib/notify'
import { clientSnippets } from '@/lib/snippets'
import { CredsModal, StatusBadge, type IssuedState } from './index'

export const Route = createFileRoute('/clients/$id')({ component: ClientDetail })

function ClientDetail() {
  const { id } = Route.useParams()
  const qc = useQueryClient()
  const client = useClient(id)
  const c = client.data
  const conns = useClientConnections(id, c?.status === 'active')
  const sys = useSystemInfo()
  const [grants, setGrants] = useState<Grant[]>([])
  const [issued, setIssued] = useState<IssuedState | null>(null)
  const [rotating, setRotating] = useState(false)
  const [days, setDays] = useState<number>(365)
  const [revoking, setRevoking] = useState(false)
  useEffect(() => {
    if (c) setGrants(c.grants)
  }, [c?.version]) // eslint-disable-line react-hooks/exhaustive-deps
  const refresh = () => qc.invalidateQueries({ queryKey: ['clients'] })

  const saveGrants = useMutation({
    mutationFn: async () => unwrap(await admin.PUT('/clients/{id}/grants', { params: { path: { id } }, body: { grants: cleanGrants(grants), version: c!.version } })),
    onSuccess: () => {
      notifyOk('Grants saved; connected sessions reconnect with the new permissions')
      void refresh()
    },
    onError: notifyErr,
  })
  const rotate = useMutation({
    mutationFn: async () => unwrap(await admin.POST('/clients/{id}/rotate', { params: { path: { id } }, body: { expires_in_days: days } })),
    onSuccess: (r) => {
      setRotating(false)
      setIssued({ name: r.client.name, creds: r.creds, grants: r.client.grants })
      void refresh()
    },
    onError: notifyErr,
  })
  const kick = useMutation({
    mutationFn: async () => unwrap(await admin.POST('/clients/{id}/kick', { params: { path: { id } } })),
    onSuccess: (r) => notifyOk(`Disconnected ${r.kicked} connection(s)`),
    onError: notifyErr,
  })
  const revoke = useMutation({
    mutationFn: async () => {
      const r = await admin.POST('/clients/{id}/revoke', { params: { path: { id } } })
      if (r.error) unwrap(r)
    },
    onSuccess: () => {
      setRevoking(false)
      notifyOk('Client revoked')
      void refresh()
    },
    onError: notifyErr,
  })
  const dirty = c && JSON.stringify(cleanGrants(grants)) !== JSON.stringify(cleanGrants(c.grants))
  const active = c?.status === 'active'

  return (
    <>
      <PageHeader
        title={c?.name ?? id}
        description={c?.description || undefined}
        actions={
          c && (
            <Can perm="clients.write">
              <Button variant="default" leftSection={<IconPlugConnectedX size={14} />} disabled={!active || !c.connections} onClick={() => kick.mutate()} loading={kick.isPending}>
                Disconnect
              </Button>
              <Button variant="default" leftSection={<IconRefresh size={14} />} disabled={c.status === 'revoked'} onClick={() => setRotating(true)}>
                Rotate credentials
              </Button>
              <Button color="red" variant="light" leftSection={<IconBan size={14} />} disabled={c.status === 'revoked'} onClick={() => setRevoking(true)}>
                Revoke
              </Button>
            </Can>
          )
        }
      />
      <ErrorAlert error={client.error} />
      {c && (
        <Stack gap="md">
          {c.expires_soon && (
            <Alert color="yellow" title="Credentials expire soon">
              They expire on {formatTime(c.expires_at)}. Rotate them and hand the new .creds to the tool.
            </Alert>
          )}
          <SimpleGrid cols={{ base: 1, md: 2 }}>
            <Card>
              <Group justify="space-between" mb="xs">
                <Title order={5}>Identity</Title>
                <StatusBadge c={c} />
              </Group>
              <Table withRowBorders={false} verticalSpacing={2}>
                <Table.Tbody>
                  {(
                    [
                      ['Client ID', c.id],
                      ['Inbox prefix', `_INBOX_${c.id}`],
                      ['User key', c.user_key],
                      ['Expires', formatTime(c.expires_at)],
                      ['Created', formatTime(c.created_at)],
                      ['Revoked', c.revoked_at ? formatTime(c.revoked_at) : '—'],
                    ] as const
                  ).map(([k, v]) => (
                    <Table.Tr key={k}>
                      <Table.Td w={120}>
                        <Text size="xs" c="dimmed">
                          {k}
                        </Text>
                      </Table.Td>
                      <Table.Td>
                        <Text size="sm" ff="monospace" style={{ wordBreak: 'break-all' }}>
                          {v}
                        </Text>
                      </Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            </Card>
            <Card>
              <Title order={5} mb="xs">
                Grants
              </Title>
              <GrantsEditor value={grants} onChange={setGrants} />
              <Can perm="clients.write">
              <Group justify="flex-end" mt="xs">
                <Button size="xs" variant="default" disabled={!dirty} onClick={() => setGrants(c.grants)}>
                  Discard
                </Button>
                <Button size="xs" disabled={!dirty || !active} loading={saveGrants.isPending} onClick={() => saveGrants.mutate()}>
                  Save grants
                </Button>
              </Group>
              </Can>
              <Text size="xs" c="dimmed" mt={6}>
                Saving takes effect without new credentials; NATS reconnects live sessions with the new permissions.
              </Text>
            </Card>
          </SimpleGrid>
          <Card>
            <Title order={5} mb="xs">
              Live connections
            </Title>
            <Table.ScrollContainer minWidth={700}>
              <Table data-testid="connections-table">
                <Table.Thead>
                  <Table.Tr>
                    <Table.Th>Address</Table.Th>
                    <Table.Th>Library</Table.Th>
                    <Table.Th>Name</Table.Th>
                    <Table.Th>RTT</Table.Th>
                    <Table.Th>Uptime</Table.Th>
                    <Table.Th ta="right">Subs</Table.Th>
                    <Table.Th ta="right">In</Table.Th>
                    <Table.Th ta="right">Out</Table.Th>
                    <Table.Th>Last active</Table.Th>
                  </Table.Tr>
                </Table.Thead>
                <Table.Tbody>
                  {(conns.data ?? []).length === 0 && (
                    <Table.Tr>
                      <Table.Td colSpan={9}>
                        <Text size="sm" c="dimmed">
                          Not connected
                        </Text>
                      </Table.Td>
                    </Table.Tr>
                  )}
                  {(conns.data ?? []).map((k) => (
                    <Table.Tr key={k.cid}>
                      <Table.Td ff="monospace">
                        {k.ip}:{k.port}
                        {k.kind && k.kind !== 'nats' && (
                          <Badge size="xs" ml={4}>
                            {k.kind}
                          </Badge>
                        )}
                      </Table.Td>
                      <Table.Td>
                        {k.lang} {k.version}
                      </Table.Td>
                      <Table.Td>{k.name || '—'}</Table.Td>
                      <Table.Td>{formatGoDuration(k.rtt)}</Table.Td>
                      <Table.Td>{k.uptime}</Table.Td>
                      <Table.Td ta="right">{k.subscriptions}</Table.Td>
                      <Table.Td ta="right">
                        {formatNumber(k.in_msgs)} · {formatBytes(k.in_bytes)}
                      </Table.Td>
                      <Table.Td ta="right">
                        {formatNumber(k.out_msgs)} · {formatBytes(k.out_bytes)}
                      </Table.Td>
                      <Table.Td>{timeAgo(k.last_active)}</Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            </Table.ScrollContainer>
          </Card>
          {sys.data && (
            <Card>
              <Title order={5} mb="xs">
                Connect
              </Title>
              <Snippets
                snippets={clientSnippets({ name: c.name, natsUrl: sys.data.connect.nats_url, publicUrl: sys.data.connect.public_url, grants: c.grants })}
              />
            </Card>
          )}
        </Stack>
      )}
      <Modal opened={rotating} onClose={() => setRotating(false)} title="Rotate credentials">
        <Stack gap="sm">
          <Text size="sm">New credentials are issued and the current ones stop working immediately.</Text>
          <NumberInput label="Valid for (days)" min={1} max={3650} value={days} onChange={(v) => setDays(Number(v) || 365)} />
          <Group justify="flex-end">
            <Button variant="default" onClick={() => setRotating(false)}>
              Cancel
            </Button>
            <Button onClick={() => rotate.mutate()} loading={rotate.isPending}>
              Rotate
            </Button>
          </Group>
        </Stack>
      </Modal>
      <ConfirmModal
        opened={revoking}
        onClose={() => setRevoking(false)}
        title={`Revoke ${c?.name ?? ''}`}
        confirmText={c?.name ?? ''}
        action="Revoke"
        loading={revoke.isPending}
        onConfirm={() => revoke.mutate()}
      >
        <Text size="sm">Revocation is permanent. Live connections are closed within seconds and the credentials stop working.</Text>
      </ConfirmModal>
      <CredsModal issued={issued} onClose={() => setIssued(null)} />
    </>
  )
}
