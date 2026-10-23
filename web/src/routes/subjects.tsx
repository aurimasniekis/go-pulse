import { ActionIcon, Alert, Badge, Button, Card, CopyButton, Drawer, Group, Loader, Stack, Table, Text, Tooltip } from '@mantine/core'
import { IconCheck, IconChevronDown, IconChevronRight, IconCopy, IconListSearch } from '@tabler/icons-react'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useState } from 'react'
import type { Schemas } from '@/api/client'
import { useSubjectInfo, useSubjectTree } from '@/api/queries'
import { ErrorAlert } from '@/components/ErrorAlert'
import { PageHeader } from '@/components/PageHeader'
import { formatBytes, formatNumber, formatTime, shellQuote, timeAgo } from '@/lib/format'

export const Route = createFileRoute('/subjects')({ component: Subjects })

type Node = Schemas['Node']

function Rows({ prefix, depth, expanded, toggle, onSelect }: { prefix: string; depth: number; expanded: Set<string>; toggle: (p: string) => void; onSelect: (n: Node) => void }) {
  const q = useSubjectTree('EVENTS', prefix)
  if (q.isLoading) {
    return (
      <Table.Tr>
        <Table.Td colSpan={6} pl={12 + depth * 18}>
          <Loader size="xs" />
        </Table.Td>
      </Table.Tr>
    )
  }
  return (
    <>
      {(q.data?.nodes ?? []).map((n) => {
        const open = expanded.has(n.path)
        return [
          <Table.Tr key={n.path} data-testid={`node-${n.path}`}>
            <Table.Td>
              <Group gap={4} wrap="nowrap" pl={depth * 18}>
                {n.children > 0 ? (
                  <ActionIcon size="xs" onClick={() => toggle(n.path)} aria-label={`${open ? 'Collapse' : 'Expand'} ${n.path}`}>
                    {open ? <IconChevronDown size={14} /> : <IconChevronRight size={14} />}
                  </ActionIcon>
                ) : (
                  <span style={{ width: 18, display: 'inline-block' }} />
                )}
                <Text
                  size="sm"
                  ff="monospace"
                  style={{ cursor: n.leaf ? 'pointer' : undefined }}
                  td={n.leaf ? 'underline dotted' : undefined}
                  onClick={n.leaf ? () => onSelect(n) : () => toggle(n.path)}
                >
                  {n.token}
                </Text>
                {n.leaf && n.children > 0 && (
                  <Badge size="xs" variant="outline" color="gray">
                    leaf
                  </Badge>
                )}
              </Group>
            </Table.Td>
            <Table.Td ta="right">{formatNumber(n.subjects)}</Table.Td>
            <Table.Td ta="right">{formatNumber(n.msgs)}</Table.Td>
            <Table.Td ta="right">{formatBytes(n.bytes)}</Table.Td>
            <Table.Td>{timeAgo(n.last_seen)}</Table.Td>
            <Table.Td>
              <Text size="sm" truncate maw={220}>
                {n.last_type || '—'}
              </Text>
            </Table.Td>
          </Table.Tr>,
          open && <Rows key={`${n.path}/children`} prefix={n.path} depth={depth + 1} expanded={expanded} toggle={toggle} onSelect={onSelect} />,
        ]
      })}
    </>
  )
}

function SubjectPanel({ subject, onClose }: { subject: string | null; onClose: () => void }) {
  const info = useSubjectInfo('EVENTS', subject)
  const navigate = useNavigate()
  const s = info.data
  const cmd = subject ? `pulse sub ${shellQuote(subject)} --since 24h` : ''
  return (
    <Drawer opened={subject !== null} onClose={onClose} position="right" size="md" title={subject ?? ''}>
      <ErrorAlert error={info.error} />
      {s && (
        <Stack gap="sm">
          <Table withRowBorders={false} verticalSpacing={2}>
            <Table.Tbody>
              {(
                [
                  ['Events', formatNumber(s.msgs)],
                  ['Size', formatBytes(s.bytes)],
                  ['First seen', formatTime(s.first_seen)],
                  ['Last seen', formatTime(s.last_seen)],
                  ['Last sequence', formatNumber(s.last_seq)],
                  ['Last type', s.last_type || '—'],
                ] as const
              ).map(([k, v]) => (
                <Table.Tr key={k}>
                  <Table.Td w={120}>
                    <Text size="xs" c="dimmed">
                      {k}
                    </Text>
                  </Table.Td>
                  <Table.Td>
                    <Text size="sm">{v}</Text>
                  </Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
          <div>
            <Text size="xs" c="dimmed" mb={4}>
              JSON keys {s.keys_sampled_at ? `(sampled ${timeAgo(s.keys_sampled_at)})` : '(not sampled yet)'}
            </Text>
            <Group gap={4}>
              {s.json_keys.length === 0 && (
                <Text size="sm" c="dimmed">
                  none
                </Text>
              )}
              {s.json_keys.map((k) => (
                <Badge key={k} variant="light" color="gray" tt="none" ff="monospace">
                  {k}
                </Badge>
              ))}
            </Group>
          </div>
          <Group gap="xs">
            <Button size="xs" leftSection={<IconListSearch size={14} />} onClick={() => void navigate({ to: '/explorer', search: { subject: [s.subject] } })}>
              Explore
            </Button>
            <CopyButton value={cmd}>
              {({ copied, copy }) => (
                <Button size="xs" variant="default" leftSection={copied ? <IconCheck size={14} /> : <IconCopy size={14} />} onClick={copy}>
                  {copied ? 'Copied' : 'Copy pulse sub'}
                </Button>
              )}
            </CopyButton>
          </Group>
        </Stack>
      )}
    </Drawer>
  )
}

function Subjects() {
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set(['evt']))
  const [selected, setSelected] = useState<string | null>(null)
  const root = useSubjectTree('EVENTS', '')
  const toggle = (p: string) =>
    setExpanded((s) => {
      const n = new Set(s)
      if (n.has(p)) n.delete(p)
      else n.add(p)
      return n
    })
  const idx = root.data?.index
  return (
    <>
      <PageHeader
        title="Subjects"
        description="Every subject seen in EVENTS, as a tree. Counts are all-time from the subject index."
        actions={
          idx && (
            <Tooltip label="Indexed subjects / index cap">
              <Badge variant="light" size="lg">
                {formatNumber(idx.indexed)} / {formatNumber(idx.cap)}
              </Badge>
            </Tooltip>
          )
        }
      />
      <ErrorAlert error={root.error} />
      {idx && idx.dropped > 0 && (
        <Alert color="yellow" mb="sm" title="Subject index is full">
          {formatNumber(idx.dropped)} subject updates were not indexed. Events are still stored and counted; only the per-subject index is
          capped. See docs/SIZING.md for the cardinality guideline.
        </Alert>
      )}
      <Card p={0}>
        <Table.ScrollContainer minWidth={800}>
          <Table data-testid="subject-tree">
            <Table.Thead>
              <Table.Tr>
                <Table.Th>Subject</Table.Th>
                <Table.Th ta="right">Subjects</Table.Th>
                <Table.Th ta="right">Events</Table.Th>
                <Table.Th ta="right">Size</Table.Th>
                <Table.Th>Last seen</Table.Th>
                <Table.Th>Last type</Table.Th>
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {root.data?.nodes.length === 0 && (
                <Table.Tr>
                  <Table.Td colSpan={6}>
                    <Text size="sm" c="dimmed">
                      No subjects indexed yet.
                    </Text>
                  </Table.Td>
                </Table.Tr>
              )}
              <Rows prefix="" depth={0} expanded={expanded} toggle={toggle} onSelect={(n) => setSelected(n.path)} />
            </Table.Tbody>
          </Table>
        </Table.ScrollContainer>
      </Card>
      <SubjectPanel subject={selected} onClose={() => setSelected(null)} />
    </>
  )
}
