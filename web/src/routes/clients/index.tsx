import { Anchor, Badge, Button, Group, Modal, NumberInput, Stack, Table, TagsInput, Text, TextInput, Textarea, Tooltip } from '@mantine/core'
import { useForm } from '@mantine/form'
import { IconPlus } from '@tabler/icons-react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { createFileRoute, Link } from '@tanstack/react-router'
import { useState } from 'react'
import { admin, unwrap, type ClientDTO, type Grant } from '@/api/client'
import { useClients, useSystemInfo } from '@/api/queries'
import { ErrorAlert } from '@/components/ErrorAlert'
import { cleanGrants, GrantsEditor } from '@/components/GrantsEditor'
import { PageHeader } from '@/components/PageHeader'
import { Can } from '@/components/Can'
import { SecretModal } from '@/components/SecretModal'
import { Snippets } from '@/components/Snippets'
import { timeAgo } from '@/lib/format'
import { notifyErr } from '@/lib/notify'
import { clientSnippets } from '@/lib/snippets'

export const Route = createFileRoute('/clients/')({ component: Clients })

export function StatusBadge({ c }: { c: ClientDTO }) {
  const color = c.status === 'active' ? (c.expires_soon ? 'yellow' : 'teal') : c.status === 'revoked' ? 'red' : 'gray'
  return (
    <Badge color={color} variant="light">
      {c.status === 'active' && c.expires_soon ? 'expires soon' : c.status}
    </Badge>
  )
}

export function grantSummary(gs: Grant[]): string {
  if (!gs.length) return 'no grants'
  return gs.map((g) => (g.action === 'blobs' ? 'blobs' : `${g.action} ${g.subject}${g.name ? ` (${g.name})` : ''}`)).join(', ')
}

export interface IssuedState {
  name: string
  creds: string
  grants: Grant[]
}

/** The once-only .creds modal with connection snippets. */
export function CredsModal({ issued, onClose }: { issued: IssuedState | null; onClose: () => void }) {
  const sys = useSystemInfo()
  return (
    <SecretModal secret={issued?.creds ?? null} title={`Credentials for ${issued?.name ?? ''}`} filename={`${issued?.name}.creds`} onClose={onClose}>
      {issued && sys.data && (
        <Snippets snippets={clientSnippets({ name: issued.name, natsUrl: sys.data.connect.nats_url, publicUrl: sys.data.connect.public_url, grants: issued.grants })} />
      )}
    </SecretModal>
  )
}

function CreateClient({ opened, onClose, onIssued }: { opened: boolean; onClose: () => void; onIssued: (i: IssuedState) => void }) {
  const qc = useQueryClient()
  const form = useForm({
    initialValues: { name: '', description: '', tags: [] as string[], grants: [] as Grant[], days: 365 },
    validate: { name: (v) => (/^[a-z0-9][a-z0-9_.-]{0,63}$/.test(v) ? null : 'lowercase letters, digits, _ . -') },
  })
  const create = useMutation({
    mutationFn: async (v: typeof form.values) =>
      unwrap(
        await admin.POST('/clients', {
          body: { name: v.name, description: v.description || undefined, tags: v.tags, grants: cleanGrants(v.grants), expires_in_days: v.days },
        }),
      ),
    onSuccess: (r, v) => {
      void qc.invalidateQueries({ queryKey: ['clients'] })
      onIssued({ name: r.client.name, creds: r.creds, grants: cleanGrants(v.grants) })
      form.reset()
      onClose()
    },
    onError: notifyErr,
  })
  return (
    <Modal opened={opened} onClose={onClose} title="Create client" size="xl">
      <form onSubmit={form.onSubmit((v) => create.mutate(v))}>
        <Stack gap="sm">
          <Group grow>
            <TextInput label="Name" placeholder="home-assistant" required {...form.getInputProps('name')} />
            <NumberInput label="Credentials valid for (days)" min={1} max={3650} {...form.getInputProps('days')} />
          </Group>
          <Textarea label="Description" autosize minRows={1} {...form.getInputProps('description')} />
          <TagsInput label="Tags" {...form.getInputProps('tags')} />
          <div>
            <Text size="xs" fw={500} mb={4}>
              Grants
            </Text>
            <GrantsEditor value={form.values.grants} onChange={(g) => form.setFieldValue('grants', g)} />
          </div>
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" loading={create.isPending}>
              Create and issue credentials
            </Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  )
}

function Clients() {
  const clients = useClients()
  const [creating, setCreating] = useState(false)
  const [issued, setIssued] = useState<IssuedState | null>(null)
  return (
    <>
      <PageHeader
        title="Clients"
        description="Tool identities on NATS. New clients have no permissions until you add grants."
        actions={
          <Can perm="clients.write">
            <Button leftSection={<IconPlus size={14} />} onClick={() => setCreating(true)}>
              Create client
            </Button>
          </Can>
        }
      />
      <ErrorAlert error={clients.error} />
      <Table.ScrollContainer minWidth={800}>
        <Table striped data-testid="clients-table">
          <Table.Thead>
            <Table.Tr>
              <Table.Th>Name</Table.Th>
              <Table.Th>Status</Table.Th>
              <Table.Th>Grants</Table.Th>
              <Table.Th ta="right">Connections</Table.Th>
              <Table.Th>Expires</Table.Th>
              <Table.Th>Created</Table.Th>
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {clients.data?.length === 0 && (
              <Table.Tr>
                <Table.Td colSpan={6}>
                  <Text size="sm" c="dimmed">
                    No clients yet.
                  </Text>
                </Table.Td>
              </Table.Tr>
            )}
            {(clients.data ?? []).map((c) => (
              <Table.Tr key={c.id}>
                <Table.Td>
                  <Anchor size="sm" fw={500} renderRoot={(p) => <Link {...p} to="/clients/$id" params={{ id: c.id }} />}>
                    {c.name}
                  </Anchor>
                  {c.tags.map((t) => (
                    <Badge key={t} size="xs" variant="outline" color="gray" ml={4}>
                      {t}
                    </Badge>
                  ))}
                </Table.Td>
                <Table.Td>
                  <StatusBadge c={c} />
                </Table.Td>
                <Table.Td>
                  <Tooltip label={grantSummary(c.grants)} multiline maw={400}>
                    <Text size="sm" ff="monospace" truncate maw={340}>
                      {grantSummary(c.grants)}
                    </Text>
                  </Tooltip>
                </Table.Td>
                <Table.Td ta="right">{c.connections}</Table.Td>
                <Table.Td>{new Date(c.expires_at).toLocaleDateString()}</Table.Td>
                <Table.Td>{timeAgo(c.created_at)}</Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      </Table.ScrollContainer>
      <CreateClient opened={creating} onClose={() => setCreating(false)} onIssued={setIssued} />
      <CredsModal issued={issued} onClose={() => setIssued(null)} />
    </>
  )
}
