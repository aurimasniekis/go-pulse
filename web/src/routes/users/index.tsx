import { Badge, Button, Group, Modal, Stack, Table, Tabs, Text, TextInput } from '@mantine/core'
import { IconMail, IconPlus, IconTrash } from '@tabler/icons-react'
import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useState } from 'react'
import { useCan } from '@/api/auth'
import type { OneTimeLink } from '@/api/client'
import { useInvitations, useUserMutations, useUsers } from '@/api/users'
import { ErrorAlert } from '@/components/ErrorAlert'
import { OneTimeLinkModal } from '@/components/OneTimeLinkModal'
import { PageHeader } from '@/components/PageHeader'
import { RoleBadges } from '@/components/RoleBadges'
import { RolePicker } from '@/components/RolePicker'
import { formatTime, timeAgo } from '@/lib/format'
import { notifyErr, notifyOk } from '@/lib/notify'

export const Route = createFileRoute('/users/')({
  validateSearch: (s: Record<string, unknown>): { tab?: 'users' | 'invitations' } => ({
    tab: s.tab === 'invitations' ? 'invitations' : undefined,
  }),
  component: Users,
})

function Users() {
  const { tab = 'users' } = Route.useSearch()
  const navigate = useNavigate({ from: Route.fullPath })
  const canManage = useCan('users.manage')
  const users = useUsers()
  const invitations = useInvitations()
  const m = useUserMutations()
  const [creating, setCreating] = useState(false)
  const [inviting, setInviting] = useState(false)
  const [form, setForm] = useState({ email: '', name: '', roles: ['viewer'] as string[] })
  const [link, setLink] = useState<{ link: OneTimeLink; title: string } | null>(null)

  return (
    <>
      <PageHeader
        title="Users"
        description="People who can sign in to this dashboard, and what their roles allow. Tools connect with clients and tokens, not users."
        actions={
          canManage && (
            <Group gap="xs">
              <Button variant="default" leftSection={<IconMail size={14} />} onClick={() => {
                setForm({ email: '', name: '', roles: ['viewer'] })
                setInviting(true)
              }}>
                Invite
              </Button>
              <Button leftSection={<IconPlus size={14} />} onClick={() => {
                setForm({ email: '', name: '', roles: ['viewer'] })
                setCreating(true)
              }}>
                New user
              </Button>
            </Group>
          )
        }
      />
      <Tabs value={tab} onChange={(v) => void navigate({ search: v === 'invitations' ? { tab: 'invitations' } : {} })}>
        <Tabs.List mb="sm">
          <Tabs.Tab value="users">Users ({users.data?.length ?? 0})</Tabs.Tab>
          <Tabs.Tab value="invitations">Invitations ({invitations.data?.length ?? 0})</Tabs.Tab>
        </Tabs.List>
        <Tabs.Panel value="users">
          <ErrorAlert error={users.error} />
          <Table striped highlightOnHover data-testid="users-table">
            <Table.Thead>
              <Table.Tr>
                <Table.Th>Name</Table.Th>
                <Table.Th>Email</Table.Th>
                <Table.Th>Roles</Table.Th>
                <Table.Th>Sign-in</Table.Th>
                <Table.Th>Status</Table.Th>
                <Table.Th>Last sign-in</Table.Th>
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {(users.data ?? []).map((u) => (
                <Table.Tr key={u.id}>
                  <Table.Td>
                    <Link to="/users/$id" params={{ id: u.id }}>
                      {u.display_name}
                    </Link>
                  </Table.Td>
                  <Table.Td>{u.email ?? '—'}</Table.Td>
                  <Table.Td>
                    <RoleBadges roles={u.roles} />
                  </Table.Td>
                  <Table.Td>
                    <Group gap={4}>
                      {u.has_password && <Badge size="xs" variant="outline">password</Badge>}
                      {u.mfa_enrolled ? (
                        <Badge size="xs" variant="outline" color="teal">2FA</Badge>
                      ) : u.mfa_required && u.has_password ? (
                        <Badge size="xs" variant="outline" color="orange">2FA pending</Badge>
                      ) : null}
                      {u.provisioned_via === 'jit' && <Badge size="xs" variant="outline" color="grape">SSO</Badge>}
                    </Group>
                  </Table.Td>
                  <Table.Td>
                    <Badge size="sm" variant="dot" color={u.enabled ? 'teal' : 'gray'}>
                      {u.enabled ? 'enabled' : 'disabled'}
                    </Badge>
                  </Table.Td>
                  <Table.Td>{u.last_login_at ? timeAgo(u.last_login_at) : 'never'}</Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        </Tabs.Panel>
        <Tabs.Panel value="invitations">
          <ErrorAlert error={invitations.error} />
          {(invitations.data ?? []).length === 0 ? (
            <Text size="sm" c="dimmed">
              No pending invitations.
            </Text>
          ) : (
            <Table striped>
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>Email</Table.Th>
                  <Table.Th>Roles</Table.Th>
                  <Table.Th>Invited by</Table.Th>
                  <Table.Th>Expires</Table.Th>
                  <Table.Th />
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {invitations.data!.map((i) => (
                  <Table.Tr key={i.id}>
                    <Table.Td>{i.email}</Table.Td>
                    <Table.Td>
                      <RoleBadges roles={i.roles} />
                    </Table.Td>
                    <Table.Td>{i.created_by}</Table.Td>
                    <Table.Td>{formatTime(i.expires_at)}</Table.Td>
                    <Table.Td>
                      {canManage && (
                        <Button size="compact-xs" variant="subtle" color="red" leftSection={<IconTrash size={12} />}
                          onClick={() => m.revokeInvitation.mutate(i.id, { onSuccess: () => notifyOk('Invitation revoked'), onError: notifyErr })}>
                          Revoke
                        </Button>
                      )}
                    </Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          )}
        </Tabs.Panel>
      </Tabs>

      <Modal opened={creating} onClose={() => setCreating(false)} title="New user">
        <form
          onSubmit={(e) => {
            e.preventDefault()
            m.create.mutate(
              { email: form.email.trim() || undefined, display_name: form.name.trim(), roles: form.roles },
              {
                onSuccess: (u) => {
                  setCreating(false)
                  notifyOk('User created')
                  void navigate({ to: '/users/$id', params: { id: u.id } })
                },
                onError: notifyErr,
              },
            )
          }}
        >
          <Stack gap="sm">
            <TextInput label="Display name" required value={form.name} onChange={(e) => setForm({ ...form, name: e.currentTarget.value })} data-autofocus />
            <TextInput label="Email" type="email" description="Needed for password sign-in; optional for single sign-on only."
              value={form.email} onChange={(e) => setForm({ ...form, email: e.currentTarget.value })} />
            <RolePicker value={form.roles} onChange={(roles) => setForm({ ...form, roles })} />
            <Text size="xs" c="dimmed">
              Next, give them a one-time password link or link their single sign-on identity on their page.
            </Text>
            <Group justify="flex-end">
              <Button variant="default" onClick={() => setCreating(false)}>
                Cancel
              </Button>
              <Button type="submit" loading={m.create.isPending}>
                Create
              </Button>
            </Group>
          </Stack>
        </form>
      </Modal>

      <Modal opened={inviting} onClose={() => setInviting(false)} title="Invite someone">
        <form
          onSubmit={(e) => {
            e.preventDefault()
            m.invite.mutate(
              { email: form.email.trim(), roles: form.roles },
              {
                onSuccess: (l) => {
                  setInviting(false)
                  setLink({ link: l, title: `Invitation for ${form.email.trim()}` })
                },
                onError: notifyErr,
              },
            )
          }}
        >
          <Stack gap="sm">
            <TextInput label="Email" type="email" required value={form.email} onChange={(e) => setForm({ ...form, email: e.currentTarget.value })}
              data-autofocus />
            <RolePicker value={form.roles} onChange={(roles) => setForm({ ...form, roles })} />
            <Text size="xs" c="dimmed">
              The invitation link is valid for 72 hours. They choose their name and password; roles that require it make them set up an
              authenticator.
            </Text>
            <Group justify="flex-end">
              <Button variant="default" onClick={() => setInviting(false)}>
                Cancel
              </Button>
              <Button type="submit" loading={m.invite.isPending}>
                Create invitation
              </Button>
            </Group>
          </Stack>
        </form>
      </Modal>
      <OneTimeLinkModal link={link?.link ?? null} title={link?.title ?? ''} onClose={() => {
        setLink(null)
        void navigate({ search: { tab: 'invitations' } })
      }} />
    </>
  )
}
