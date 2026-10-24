import { ActionIcon, Badge, Button, Card, Group, Modal, NumberInput, SimpleGrid, Stack, Table, Tabs, Text, TextInput, Title, Tooltip } from '@mantine/core'
import { useForm } from '@mantine/form'
import { IconBan, IconLockOpen, IconPlus, IconShieldCheck, IconTrash } from '@tabler/icons-react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { createFileRoute } from '@tanstack/react-router'
import { useState, type ReactNode } from 'react'
import { admin, unwrap } from '@/api/client'
import { useSecurity } from '@/api/queries'
import { ErrorAlert } from '@/components/ErrorAlert'
import { PageHeader } from '@/components/PageHeader'
import { Can } from '@/components/Can'
import { formatTime, timeAgo } from '@/lib/format'
import { notifyErr, notifyOk } from '@/lib/notify'

export const Route = createFileRoute('/security')({ component: Security })

function untilText(iso?: string): string {
  if (!iso) return 'no expiry'
  const ms = new Date(iso).getTime() - Date.now()
  if (ms <= 0) return 'expired'
  const h = ms / 3_600_000
  return h >= 48 ? `${Math.round(h / 24)}d left` : h >= 1 ? `${Math.round(h)}h left` : `${Math.max(1, Math.round(ms / 60_000))}m left`
}

function Flag({ label, on, hint }: { label: string; on: boolean; hint: string }) {
  return (
    <Tooltip label={hint} multiline maw={320}>
      <Group gap={6} wrap="nowrap">
        <Badge variant="dot" color={on ? 'teal' : 'gray'}>
          {label}
        </Badge>
      </Group>
    </Tooltip>
  )
}

function Section({ title, children, action }: { title: string; children: ReactNode; action?: ReactNode }) {
  return (
    <Card>
      <Group justify="space-between" mb="xs">
        <Title order={5}>{title}</Title>
        {action}
      </Group>
      {children}
    </Card>
  )
}

function Security() {
  const sec = useSecurity()
  const qc = useQueryClient()
  const [banning, setBanning] = useState(false)
  const [allowing, setAllowing] = useState(false)
  const refresh = () => qc.invalidateQueries({ queryKey: ['security'] })
  const banForm = useForm({ initialValues: { network: '', hours: 24, reason: '' } })
  const allowForm = useForm({ initialValues: { network: '', note: '' } })
  const ban = useMutation({
    mutationFn: async (v: typeof banForm.values) =>
      unwrap(await admin.POST('/security/bans', { body: { network: v.network.trim(), duration_hours: v.hours, reason: v.reason || 'manual' } })),
    onSuccess: (b) => {
      notifyOk(`Banned ${b.network}`)
      setBanning(false)
      banForm.reset()
      void refresh()
    },
    onError: notifyErr,
  })
  const lift = useMutation({
    mutationFn: async (network: string) => {
      const r = await admin.POST('/security/bans/lift', { body: { network } })
      if (r.error) unwrap(r)
    },
    onSuccess: () => {
      notifyOk('Ban lifted')
      void refresh()
    },
    onError: notifyErr,
  })
  const allow = useMutation({
    mutationFn: async (v: typeof allowForm.values) => {
      const r = await admin.POST('/security/allowlist', { body: { network: v.network.trim(), note: v.note || undefined } })
      if (r.error) unwrap(r)
    },
    onSuccess: () => {
      notifyOk('Added to the allowlist')
      setAllowing(false)
      allowForm.reset()
      void refresh()
    },
    onError: notifyErr,
  })
  const disallow = useMutation({
    mutationFn: async (network: string) => {
      const r = await admin.POST('/security/allowlist/remove', { body: { network } })
      if (r.error) unwrap(r)
    },
    onSuccess: () => void refresh(),
    onError: notifyErr,
  })
  const s = sec.data
  const e = s?.edge
  return (
    <>
      <PageHeader
        title="Security"
        description="Automatic bans from failed NATS logins, bad HTTP tokens and bad webhook signatures, enforced in pulsed and nftables."
        actions={
          <Can perm="security.write">
            <Button variant="default" leftSection={<IconShieldCheck size={14} />} onClick={() => setAllowing(true)}>
              Allow network
            </Button>
            <Button color="red" variant="light" leftSection={<IconBan size={14} />} onClick={() => setBanning(true)}>
              Ban network
            </Button>
          </Can>
        }
      />
      <ErrorAlert error={sec.error} />
      {s && e && (
        <Stack gap="md">
          <Card>
            <SimpleGrid cols={{ base: 2, md: 6 }} spacing="sm">
              <Flag label="Origin TLS" on={e.tls} hint="The public listener serves the Cloudflare Origin CA certificate." />
              <Flag label="Origin pulls (mTLS)" on={e.aop} hint="Only connections presenting the AOP client certificate are accepted." />
              <Flag label="nftables feed" on={e.ban_feed} hint="banagent reads bans from pulsed and mirrors them into nftables." />
              <Flag label="Cloudflare list" on={e.cloudflare_list_sync} hint="Bans of an hour or more are copied to a Cloudflare IP list." />
              <Text size="sm">
                {e.cloudflare_ranges} Cloudflare ranges <Text span c="dimmed" size="xs">({timeAgo(e.cloudflare_updated)})</Text>
              </Text>
              <Text size="sm">Origin cert: {e.origin_not_after ? `expires ${new Date(e.origin_not_after).toLocaleDateString()}` : '—'}</Text>
            </SimpleGrid>
          </Card>
          <Section title={`Active bans (${s.active.length})`}>
            <Table.ScrollContainer minWidth={760}>
              <Table data-testid="bans-table">
                <Table.Thead>
                  <Table.Tr>
                    <Table.Th>Network</Table.Th>
                    <Table.Th>Source</Table.Th>
                    <Table.Th>Reason</Table.Th>
                    <Table.Th>Since</Table.Th>
                    <Table.Th>Expires</Table.Th>
                    <Table.Th ta="right">Offence</Table.Th>
                    <Table.Th />
                  </Table.Tr>
                </Table.Thead>
                <Table.Tbody>
                  {s.active.length === 0 && (
                    <Table.Tr>
                      <Table.Td colSpan={7}>
                        <Text size="sm" c="dimmed">
                          No active bans.
                        </Text>
                      </Table.Td>
                    </Table.Tr>
                  )}
                  {s.active.map((b) => (
                    <Table.Tr key={b.network}>
                      <Table.Td ff="monospace">{b.network}</Table.Td>
                      <Table.Td>
                        <Badge variant="light" color={b.manual ? 'grape' : 'orange'}>
                          {b.source}
                        </Badge>
                      </Table.Td>
                      <Table.Td>{b.reason}</Table.Td>
                      <Table.Td>{timeAgo(b.created_at)}</Table.Td>
                      <Table.Td>{untilText(b.expires_at)}</Table.Td>
                      <Table.Td ta="right">{b.offences}</Table.Td>
                      <Table.Td>
                        <Can perm="security.write">
                          <Tooltip label="Lift ban">
                            <ActionIcon aria-label={`Lift ban on ${b.network}`} onClick={() => lift.mutate(b.network)}>
                              <IconLockOpen size={14} />
                            </ActionIcon>
                          </Tooltip>
                        </Can>
                      </Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            </Table.ScrollContainer>
          </Section>
          <Tabs defaultValue="failures" keepMounted={false}>
            <Tabs.List mb="xs">
              <Tabs.Tab value="failures">Recent failures ({s.failures.length})</Tabs.Tab>
              <Tabs.Tab value="offenders">Repeat offenders ({s.offenders.length})</Tabs.Tab>
              <Tabs.Tab value="allow">Allowlist ({s.allowlist.length + e.never_ban.length})</Tabs.Tab>
              <Tabs.Tab value="history">Ban history ({s.history.length})</Tabs.Tab>
            </Tabs.List>
            <Tabs.Panel value="failures">
              <Table data-testid="failures-table">
                <Table.Thead>
                  <Table.Tr>
                    <Table.Th>Time</Table.Th>
                    <Table.Th>Address</Table.Th>
                    <Table.Th>Source</Table.Th>
                    <Table.Th>Detail</Table.Th>
                  </Table.Tr>
                </Table.Thead>
                <Table.Tbody>
                  {s.failures.slice(0, 200).map((f, i) => (
                    <Table.Tr key={i}>
                      <Table.Td ff="monospace">{formatTime(f.time)}</Table.Td>
                      <Table.Td ff="monospace">{f.ip}</Table.Td>
                      <Table.Td>{f.source}</Table.Td>
                      <Table.Td>
                        <Text size="sm" c="dimmed" span>
                          {f.detail}
                        </Text>{' '}
                        {f.ignored && (
                          <Badge size="xs" color="gray" variant="outline">
                            {f.ignored}
                          </Badge>
                        )}
                      </Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            </Tabs.Panel>
            <Tabs.Panel value="offenders">
              <Table>
                <Table.Thead>
                  <Table.Tr>
                    <Table.Th>Network</Table.Th>
                    <Table.Th ta="right">Bans</Table.Th>
                    <Table.Th>Last ban ends</Table.Th>
                  </Table.Tr>
                </Table.Thead>
                <Table.Tbody>
                  {s.offenders.map((o) => (
                    <Table.Tr key={o.network}>
                      <Table.Td ff="monospace">{o.network}</Table.Td>
                      <Table.Td ta="right">{o.count}</Table.Td>
                      <Table.Td>{formatTime(o.last_at)}</Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            </Tabs.Panel>
            <Tabs.Panel value="allow">
              <Table data-testid="allow-table">
                <Table.Thead>
                  <Table.Tr>
                    <Table.Th>Network</Table.Th>
                    <Table.Th>Note</Table.Th>
                    <Table.Th>Added</Table.Th>
                    <Table.Th />
                  </Table.Tr>
                </Table.Thead>
                <Table.Tbody>
                  {s.allowlist.map((a) => (
                    <Table.Tr key={a.network}>
                      <Table.Td ff="monospace">{a.network}</Table.Td>
                      <Table.Td>{a.note}</Table.Td>
                      <Table.Td>
                        {timeAgo(a.created_at)} by {a.actor}
                      </Table.Td>
                      <Table.Td>
                        <Can perm="security.write">
                          <ActionIcon color="red" aria-label={`Remove ${a.network} from the allowlist`} onClick={() => disallow.mutate(a.network)}>
                            <IconTrash size={14} />
                          </ActionIcon>
                        </Can>
                      </Table.Td>
                    </Table.Tr>
                  ))}
                  {e.never_ban.map((n) => (
                    <Table.Tr key={`cfg-${n}`}>
                      <Table.Td ff="monospace">{n}</Table.Td>
                      <Table.Td>
                        <Badge size="xs" variant="outline" color="gray">
                          configuration
                        </Badge>
                      </Table.Td>
                      <Table.Td>—</Table.Td>
                      <Table.Td />
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
              <Text size="xs" c="dimmed" mt="xs">
                Always exempt: loopback, private and link-local ranges, unique-local IPv6 and Cloudflare&apos;s published ranges.
              </Text>
            </Tabs.Panel>
            <Tabs.Panel value="history">
              <Table>
                <Table.Thead>
                  <Table.Tr>
                    <Table.Th>Network</Table.Th>
                    <Table.Th>Source</Table.Th>
                    <Table.Th>Reason</Table.Th>
                    <Table.Th>From</Table.Th>
                    <Table.Th>Until</Table.Th>
                  </Table.Tr>
                </Table.Thead>
                <Table.Tbody>
                  {s.history.map((b) => (
                    <Table.Tr key={`${b.network}-${b.created_at}`}>
                      <Table.Td ff="monospace">{b.network}</Table.Td>
                      <Table.Td>{b.source}</Table.Td>
                      <Table.Td>{b.reason}</Table.Td>
                      <Table.Td>{formatTime(b.created_at)}</Table.Td>
                      <Table.Td>{formatTime(b.expires_at)}</Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            </Tabs.Panel>
          </Tabs>
        </Stack>
      )}
      <Modal opened={banning} onClose={() => setBanning(false)} title="Ban network">
        <form onSubmit={banForm.onSubmit((v) => ban.mutate(v))}>
          <Stack gap="sm">
            <TextInput label="IP or CIDR" placeholder="203.0.113.7 or 203.0.113.0/24" required {...banForm.getInputProps('network')} />
            <NumberInput label="Duration (hours)" description="0 = no expiry" min={0} max={8760} {...banForm.getInputProps('hours')} />
            <TextInput label="Reason" {...banForm.getInputProps('reason')} />
            <Group justify="flex-end">
              <Button variant="default" onClick={() => setBanning(false)}>
                Cancel
              </Button>
              <Button type="submit" color="red" loading={ban.isPending}>
                Ban
              </Button>
            </Group>
          </Stack>
        </form>
      </Modal>
      <Modal opened={allowing} onClose={() => setAllowing(false)} title="Allow network">
        <form onSubmit={allowForm.onSubmit((v) => allow.mutate(v))}>
          <Stack gap="sm">
            <TextInput label="IP or CIDR" required {...allowForm.getInputProps('network')} />
            <TextInput label="Note" placeholder="home office" {...allowForm.getInputProps('note')} />
            <Text size="xs" c="dimmed">
              Active bans inside this network are lifted.
            </Text>
            <Group justify="flex-end">
              <Button variant="default" onClick={() => setAllowing(false)}>
                Cancel
              </Button>
              <Button type="submit" leftSection={<IconPlus size={14} />} loading={allow.isPending}>
                Allow
              </Button>
            </Group>
          </Stack>
        </form>
      </Modal>
    </>
  )
}
