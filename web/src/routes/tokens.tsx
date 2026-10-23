import { ActionIcon, Badge, Button, Group, Modal, NumberInput, Stack, Table, TagsInput, Text, TextInput, Tooltip } from '@mantine/core'
import { useForm } from '@mantine/form'
import { IconBan, IconPlus } from '@tabler/icons-react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { createFileRoute } from '@tanstack/react-router'
import { useState } from 'react'
import { admin, unwrap, type Grant, type TokenDTO } from '@/api/client'
import { useSystemInfo, useTokens } from '@/api/queries'
import { ConfirmModal } from '@/components/ConfirmModal'
import { ErrorAlert } from '@/components/ErrorAlert'
import { cleanGrants, GrantsEditor } from '@/components/GrantsEditor'
import { PageHeader } from '@/components/PageHeader'
import { SecretModal } from '@/components/SecretModal'
import { Snippets } from '@/components/Snippets'
import { timeAgo } from '@/lib/format'
import { notifyErr, notifyOk } from '@/lib/notify'
import { tokenSnippets } from '@/lib/snippets'
import { grantSummary } from './clients/index'

export const Route = createFileRoute('/tokens')({ component: Tokens })

function Tokens() {
  const tokens = useTokens()
  const sys = useSystemInfo()
  const qc = useQueryClient()
  const [creating, setCreating] = useState(false)
  const [secret, setSecret] = useState<{ value: string; grants: Grant[] } | null>(null)
  const [revoking, setRevoking] = useState<TokenDTO | null>(null)
  const form = useForm({
    initialValues: { name: '', description: '', grants: [{ action: 'publish', subject: '' }] as Grant[], cidrs: [] as string[], days: 365 },
    validate: { name: (v) => (/^[a-z0-9][a-z0-9_.-]{0,63}$/.test(v) ? null : 'lowercase letters, digits, _ . -') },
  })
  const create = useMutation({
    mutationFn: async (v: typeof form.values) =>
      unwrap(
        await admin.POST('/tokens', {
          body: { name: v.name, description: v.description || undefined, grants: cleanGrants(v.grants), cidrs: v.cidrs, expires_in_days: v.days },
        }),
      ),
    onSuccess: (r, v) => {
      void qc.invalidateQueries({ queryKey: ['tokens'] })
      setCreating(false)
      form.reset()
      setSecret({ value: r.secret, grants: cleanGrants(v.grants) })
    },
    onError: notifyErr,
  })
  const revoke = useMutation({
    mutationFn: async (id: string) => {
      const r = await admin.POST('/tokens/{id}/revoke', { params: { path: { id } } })
      if (r.error) unwrap(r)
    },
    onSuccess: () => {
      setRevoking(null)
      notifyOk('Token revoked')
      void qc.invalidateQueries({ queryKey: ['tokens'] })
    },
    onError: notifyErr,
  })
  return (
    <>
      <PageHeader
        title="HTTP tokens"
        description="Bearer tokens for tools that use the public HTTP API instead of NATS. Stored only as hashes."
        actions={
          <Button leftSection={<IconPlus size={14} />} onClick={() => setCreating(true)}>
            Create token
          </Button>
        }
      />
      <ErrorAlert error={tokens.error} />
      <Table.ScrollContainer minWidth={800}>
        <Table striped data-testid="tokens-table">
          <Table.Thead>
            <Table.Tr>
              <Table.Th>Name</Table.Th>
              <Table.Th>Prefix</Table.Th>
              <Table.Th>Status</Table.Th>
              <Table.Th>Grants</Table.Th>
              <Table.Th>Networks</Table.Th>
              <Table.Th>Last used</Table.Th>
              <Table.Th>Expires</Table.Th>
              <Table.Th />
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {tokens.data?.length === 0 && (
              <Table.Tr>
                <Table.Td colSpan={8}>
                  <Text size="sm" c="dimmed">
                    No tokens yet.
                  </Text>
                </Table.Td>
              </Table.Tr>
            )}
            {(tokens.data ?? []).map((t) => (
              <Table.Tr key={t.id}>
                <Table.Td fw={500}>{t.name}</Table.Td>
                <Table.Td ff="monospace">pulse_{t.prefix}_…</Table.Td>
                <Table.Td>
                  <Badge color={t.status === 'active' ? 'teal' : t.status === 'revoked' ? 'red' : 'gray'} variant="light">
                    {t.status}
                  </Badge>
                </Table.Td>
                <Table.Td>
                  <Text size="sm" ff="monospace" truncate maw={280}>
                    {grantSummary(t.grants)}
                  </Text>
                </Table.Td>
                <Table.Td ff="monospace">{t.cidrs.join(', ') || 'any'}</Table.Td>
                <Table.Td>{t.last_used_at ? `${timeAgo(t.last_used_at)} from ${t.last_used_ip}` : 'never'}</Table.Td>
                <Table.Td>{new Date(t.expires_at).toLocaleDateString()}</Table.Td>
                <Table.Td>
                  {t.status === 'active' && (
                    <Tooltip label="Revoke">
                      <ActionIcon color="red" aria-label={`Revoke ${t.name}`} onClick={() => setRevoking(t)}>
                        <IconBan size={14} />
                      </ActionIcon>
                    </Tooltip>
                  )}
                </Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      </Table.ScrollContainer>
      <Modal opened={creating} onClose={() => setCreating(false)} title="Create HTTP token" size="xl">
        <form onSubmit={form.onSubmit((v) => create.mutate(v))}>
          <Stack gap="sm">
            <Group grow>
              <TextInput label="Name" placeholder="github-actions" required {...form.getInputProps('name')} />
              <NumberInput label="Valid for (days)" min={1} max={3650} {...form.getInputProps('days')} />
            </Group>
            <TextInput label="Description" {...form.getInputProps('description')} />
            <TagsInput label="Allowed networks" description="IPs or CIDRs; empty allows any source" placeholder="203.0.113.0/24" {...form.getInputProps('cidrs')} />
            <div>
              <Text size="xs" fw={500} mb={4}>
                Grants
              </Text>
              <GrantsEditor value={form.values.grants} onChange={(g) => form.setFieldValue('grants', g)} actions={['publish', 'subscribe', 'request']} />
            </div>
            <Group justify="flex-end">
              <Button variant="default" onClick={() => setCreating(false)}>
                Cancel
              </Button>
              <Button type="submit" loading={create.isPending}>
                Create
              </Button>
            </Group>
          </Stack>
        </form>
      </Modal>
      <SecretModal secret={secret?.value ?? null} title="New HTTP token" onClose={() => setSecret(null)}>
        {secret && <Snippets snippets={tokenSnippets(sys.data?.connect.public_url ?? '', secret.grants)} />}
      </SecretModal>
      <ConfirmModal
        opened={revoking !== null}
        onClose={() => setRevoking(null)}
        title={`Revoke ${revoking?.name ?? ''}`}
        confirmText={revoking?.name ?? ''}
        action="Revoke"
        loading={revoke.isPending}
        onConfirm={() => revoking && revoke.mutate(revoking.id)}
      >
        <Text size="sm">Requests with this token start failing immediately.</Text>
      </ConfirmModal>
    </>
  )
}
