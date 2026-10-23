import { Alert, Badge, Button, Card, Code, CopyButton, Group, SimpleGrid, Stack, Table, Text, Title, Tooltip } from '@mantine/core'
import { IconAlertTriangle, IconCertificate, IconCheck, IconCopy } from '@tabler/icons-react'
import { createFileRoute } from '@tanstack/react-router'
import type { ReactNode } from 'react'
import { useCertificates, useSystemInfo } from '@/api/queries'
import { ErrorAlert } from '@/components/ErrorAlert'
import { PageHeader } from '@/components/PageHeader'
import { expiryBadge } from '@/lib/certs'
import { formatBytes, formatNumber, formatTime, timeAgo } from '@/lib/format'

export const Route = createFileRoute('/system')({ component: System })

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <Group justify="space-between" wrap="nowrap" gap="md">
      <Text size="sm" c="dimmed">
        {label}
      </Text>
      <Text size="sm" ta="right" component="div">
        {children}
      </Text>
    </Group>
  )
}

function System() {
  const info = useSystemInfo()
  const certs = useCertificates()
  const s = info.data
  const acme = certs.data?.acme
  return (
    <>
      <PageHeader title="System" description="Server, NATS and certificate status." />
      <ErrorAlert error={info.error ?? certs.error} />
      <SimpleGrid cols={{ base: 1, md: 3 }} mb="sm">
        <Card>
          <Title order={5} mb="xs">
            pulsed
          </Title>
          <Stack gap={6}>
            <Row label="Version">
              <Code>{s?.version ?? '…'}</Code>
            </Row>
            <Row label="Profile">{s?.profile}</Row>
            <Row label="Admin auth">{s?.auth_mode}</Row>
            <Row label="Ready">
              <Badge variant="dot" color={s?.ready ? 'teal' : 'red'}>
                {s?.ready ? 'ready' : 'not ready'}
              </Badge>
            </Row>
          </Stack>
        </Card>
        <Card>
          <Title order={5} mb="xs">
            NATS
          </Title>
          <Stack gap={6}>
            <Row label="Connection">
              <Badge variant="dot" color={s?.nats.connected ? 'teal' : 'red'}>
                {s?.nats.connected ? 'connected' : 'disconnected'}
              </Badge>
            </Row>
            <Row label="Server">{s?.nats.server_name}</Row>
            <Row label="Version">{s?.nats.version}</Row>
            <Row label="RTT">{s ? `${s.nats.rtt_ms.toFixed(2)} ms` : ''}</Row>
          </Stack>
        </Card>
        <Card>
          <Title order={5} mb="xs">
            JetStream
          </Title>
          <Stack gap={6}>
            <Row label="Storage">
              {formatBytes(s?.jetstream?.storage)}
              {s?.jetstream?.config.max_storage ? ` of ${formatBytes(s.jetstream.config.max_storage)}` : ''}
            </Row>
            <Row label="Memory">{formatBytes(s?.jetstream?.memory)}</Row>
            <Row label="Streams / consumers">
              {s?.jetstream ? `${s.jetstream.streams} / ${s.jetstream.consumers}` : ''}
            </Row>
            <Row label="Messages">{formatNumber(s?.jetstream?.messages)}</Row>
            <Row label="API errors">{s?.jetstream ? `${formatNumber(s.jetstream.api.errors)} of ${formatNumber(s.jetstream.api.total)}` : ''}</Row>
          </Stack>
        </Card>
      </SimpleGrid>

      <Card mb="sm">
        <Title order={5} mb="xs">
          Connect
        </Title>
        <Stack gap={6}>
          <Row label="NATS (tools)">
            <Code>{s?.connect.nats_url}</Code>
          </Row>
          <Row label="Public HTTP API">
            <Code>{s?.connect.public_url || '—'}</Code>
          </Row>
          <Row label="Internal CA">
            <CopyButton value={s?.connect.ca_cert ?? ''}>
              {({ copied, copy }) => (
                <Button size="compact-xs" variant="default" leftSection={copied ? <IconCheck size={12} /> : <IconCopy size={12} />} onClick={copy}>
                  {copied ? 'Copied' : 'Copy PEM'}
                </Button>
              )}
            </CopyButton>
          </Row>
        </Stack>
      </Card>

      {acme && (
        <Card mb="sm" data-testid="acme-status">
          <Group justify="space-between" mb="xs">
            <Title order={5}>Let's Encrypt for {acme.domain}</Title>
            <Badge variant="dot" color={acme.last_error ? 'red' : acme.verified ? 'teal' : 'yellow'}>
              {acme.last_error ? 'renewal failing' : acme.verified ? 'served by NATS' : 'not verified yet'}
            </Badge>
          </Group>
          {acme.last_error && (
            <Alert color="red" icon={<IconAlertTriangle size={16} />} mb="xs" title="Last attempt failed">
              <Text size="sm" ff="monospace">
                {acme.last_error}
              </Text>
            </Alert>
          )}
          <SimpleGrid cols={{ base: 1, md: 2 }} spacing="xs">
            <Row label="Directory">
              <Code>{acme.directory}</Code>
            </Row>
            <Row label="Serial">
              <Code>{acme.serial || '—'}</Code>
            </Row>
            <Row label="Last attempt">{acme.last_attempt ? timeAgo(acme.last_attempt) : 'none since start'}</Row>
            <Row label="Last success">{acme.last_success ? timeAgo(acme.last_success) : 'none since start'}</Row>
          </SimpleGrid>
        </Card>
      )}

      <Card>
        <Group gap={6} mb="xs">
          <IconCertificate size={16} />
          <Title order={5}>Certificates</Title>
        </Group>
        <Table.ScrollContainer minWidth={760}>
          <Table data-testid="certificates" verticalSpacing={6}>
            <Table.Thead>
              <Table.Tr>
                <Table.Th>Certificate</Table.Th>
                <Table.Th>Names</Table.Th>
                <Table.Th>Issuer</Table.Th>
                <Table.Th>Expires</Table.Th>
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {(certs.data?.certificates ?? []).map((c) => {
                const b = expiryBadge(c)
                return (
                  <Table.Tr key={c.name}>
                    <Table.Td>
                      <Text size="sm" fw={500}>
                        {c.name}
                      </Text>
                      <Text size="xs" c="dimmed">
                        {c.purpose}
                      </Text>
                    </Table.Td>
                    <Table.Td>
                      <Text size="sm" ff="monospace">
                        {c.dns_names.length ? c.dns_names.join(', ') : c.subject || '—'}
                      </Text>
                    </Table.Td>
                    <Table.Td>
                      <Text size="sm">{c.issuer || '—'}</Text>
                    </Table.Td>
                    <Table.Td>
                      <Group gap={6} wrap="nowrap">
                        <Badge variant="light" color={b.color}>
                          {b.label}
                        </Badge>
                        {c.not_after && (
                          <Tooltip label={`Serial ${c.serial}`}>
                            <Text size="xs" c="dimmed" ff="monospace">
                              {formatTime(c.not_after)}
                            </Text>
                          </Tooltip>
                        )}
                      </Group>
                      {c.error && (
                        <Text size="xs" c="red">
                          {c.error}
                        </Text>
                      )}
                    </Table.Td>
                  </Table.Tr>
                )
              })}
            </Table.Tbody>
          </Table>
        </Table.ScrollContainer>
      </Card>
    </>
  )
}
