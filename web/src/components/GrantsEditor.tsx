import { ActionIcon, Button, Group, Select, Stack, Text, TextInput, Tooltip } from '@mantine/core'
import { IconPlus, IconTrash } from '@tabler/icons-react'
import type { Grant } from '@/api/client'

type Action = Grant['action']

const help: Record<Action, string> = {
  publish: 'Publish to the subject pattern',
  subscribe: 'Subscribe live and replay history (EVENTS) on the filter',
  request: 'Send requests to a service subject',
  reply: 'Serve requests on a subject',
  durable: 'Named durable consumer <client-id>_<name> on the filter',
  blobs: 'Read and write the blobs object store',
}

const allActions: Action[] = ['publish', 'subscribe', 'request', 'reply', 'durable', 'blobs']

/** Edits a list of grants (PL-AUTH-005). Validation happens server-side; obvious mistakes are flagged inline. */
export function GrantsEditor({ value, onChange, actions = allActions }: { value: Grant[]; onChange: (g: Grant[]) => void; actions?: Action[] }) {
  const set = (i: number, patch: Partial<Grant>) => onChange(value.map((g, j) => (j === i ? { ...g, ...patch } : g)))
  return (
    <Stack gap={6} data-testid="grants-editor">
      {value.length === 0 && (
        <Text size="sm" c="dimmed">
          No grants: the client can connect but cannot publish or subscribe to anything.
        </Text>
      )}
      {value.map((g, i) => (
        <Group key={i} gap={6} wrap="nowrap" align="flex-start">
          <Select
            w={130}
            aria-label={`Grant ${i + 1} action`}
            data={actions.map((a) => ({ value: a, label: a }))}
            value={g.action}
            onChange={(v) => v && set(i, { action: v as Action, name: v === 'durable' ? g.name : undefined, subject: v === 'blobs' ? undefined : g.subject })}
            allowDeselect={false}
          />
          <Tooltip label={help[g.action]} openDelay={500}>
            <TextInput
              style={{ flex: 1 }}
              aria-label={`Grant ${i + 1} subject`}
              placeholder={g.action === 'blobs' ? '(no subject)' : 'evt.home.>'}
              disabled={g.action === 'blobs'}
              value={g.subject ?? ''}
              onChange={(e) => set(i, { subject: e.currentTarget.value })}
              error={g.action !== 'blobs' && g.subject && /^[$>*]|^_INBOX/.test(g.subject) ? 'Not grantable' : undefined}
              ff="monospace"
            />
          </Tooltip>
          {g.action === 'durable' && (
            <TextInput w={140} aria-label={`Grant ${i + 1} durable name`} placeholder="name" value={g.name ?? ''} onChange={(e) => set(i, { name: e.currentTarget.value })} />
          )}
          <ActionIcon mt={4} color="red" aria-label={`Remove grant ${i + 1}`} onClick={() => onChange(value.filter((_, j) => j !== i))}>
            <IconTrash size={14} />
          </ActionIcon>
        </Group>
      ))}
      <Group>
        <Button size="xs" variant="subtle" leftSection={<IconPlus size={14} />} onClick={() => onChange([...value, { action: actions[0], subject: '' }])}>
          Add grant
        </Button>
      </Group>
    </Stack>
  )
}

/** Drops empty optional fields before sending grants to the API. */
export function cleanGrants(gs: Grant[]): Grant[] {
  return gs.map((g) => ({
    action: g.action,
    ...(g.action !== 'blobs' && g.subject ? { subject: g.subject.trim() } : {}),
    ...(g.action === 'durable' && g.name ? { name: g.name.trim() } : {}),
    ...(g.stream ? { stream: g.stream } : {}),
  }))
}
