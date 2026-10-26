import { Anchor, Badge, Button, Card, Code, Group, Modal, Select, SimpleGrid, Stack, Switch, Table, Text, TextInput, Title } from '@mantine/core'
import { IconKey, IconLink, IconLogout, IconTrash } from '@tabler/icons-react'
import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { useCan, useMe } from '@/api/auth'
import type { OneTimeLink } from '@/api/client'
import { useSSOProviders, useUser, useUserIdentities, useUserMutations, useUserSessions, useUserTokens } from '@/api/users'
import { ConfirmModal } from '@/components/ConfirmModal'
import { ErrorAlert } from '@/components/ErrorAlert'
import { OneTimeLinkModal } from '@/components/OneTimeLinkModal'
import { PageHeader } from '@/components/PageHeader'
import { RoleBadges } from '@/components/RoleBadges'
import { RolePicker } from '@/components/RolePicker'
import { formatTime, timeAgo } from '@/lib/format'
import { notifyErr, notifyOk } from '@/lib/notify'

export const Route = createFileRoute('/users/$id')({ component: UserPage })

function UserPage() {
  const { id } = Route.useParams()
  const navigate = useNavigate()
  const me = useMe().data
  const canManage = useCan('users.manage')
  const user = useUser(id)
  const m = useUserMutations()
  const [edit, setEdit] = useState({ name: '', roles: [] as string[], enabled: true, mfa: false })
  const [link, setLink] = useState<{ link: OneTimeLink; title: string } | null>(null)
  const [deleting, setDeleting] = useState(false)
  const u = user.data
  useEffect(() => {
    if (u) {
      setEdit({
        name: u.display_name,
        roles: [...new Set(u.role_assignments.filter((a) => a.source === 'manual').map((a) => a.role))],
        enabled: u.enabled,
        mfa: u.mfa_required,
      })
    }
  }, [u])
  if (!u) {
    return (
      <>
        <PageHeader title="User" />
        <ErrorAlert error={user.error} />
      </>
    )
  }
  const self = me?.user.id === u.id
  const sso = u.role_assignments.filter((a) => a.source === 'sso')
  return (
    <>
      <PageHeader
        title={u.display_name}
        description={u.email ?? 'No email (single sign-on only)'}
        actions={
          <Anchor component={Link} to="/users" size="sm">
            All users
          </Anchor>
        }
      />
      <SimpleGrid cols={{ base: 1, lg: 2 }} spacing="sm" mb="sm">
        <Card>
          <Title order={5} mb="xs">
            Access
          </Title>
          <form
            onSubmit={(e) => {
              e.preventDefault()
              m.update.mutate(
                { id, body: { display_name: edit.name.trim(), roles: edit.roles, enabled: edit.enabled, mfa_required: edit.mfa } },
                { onSuccess: () => notifyOk('Saved; the user signs in again for changed roles'), onError: notifyErr },
              )
            }}
          >
            <Stack gap="sm">
              <TextInput label="Display name" value={edit.name} disabled={!canManage} onChange={(e) => setEdit({ ...edit, name: e.currentTarget.value })} />
              <RolePicker value={edit.roles} onChange={(roles) => setEdit({ ...edit, roles })} />
              {sso.length > 0 && (
                <Text size="xs" c="dimmed">
                  Also from single sign-on groups (updated at every SSO sign-in): <RoleBadges roles={[...new Set(sso.map((a) => a.role))]} />
                </Text>
              )}
              <Switch label="Enabled" checked={edit.enabled} disabled={!canManage || self} onChange={(e) => setEdit({ ...edit, enabled: e.currentTarget.checked })}
                description={self ? 'You cannot disable your own account.' : 'Disabling signs the user out everywhere.'} />
              <Switch label="Require an authenticator" checked={edit.mfa} disabled={!canManage} onChange={(e) => setEdit({ ...edit, mfa: e.currentTarget.checked })}
                description="Administrators and operators with a password always need one." />
              {canManage && (
                <Group justify="flex-end">
                  <Button type="submit" size="xs" loading={m.update.isPending}>
                    Save
                  </Button>
                </Group>
              )}
            </Stack>
          </form>
        </Card>
        <Card>
          <Title order={5} mb="xs">
            Sign-in
          </Title>
          <Stack gap="xs">
            <Group gap="xs">
              <Badge variant="light" color={u.has_password ? 'indigo' : 'gray'}>
                {u.has_password ? 'password set' : 'no password'}
              </Badge>
              <Badge variant="light" color={u.mfa_enrolled ? 'teal' : 'gray'}>
                {u.mfa_enrolled ? 'authenticator' : 'no authenticator'}
              </Badge>
              <Badge variant="light" color="gray">
                via {u.provisioned_via}
              </Badge>
            </Group>
            <Text size="sm" c="dimmed">
              Created {formatTime(u.created_at)} · last sign-in {u.last_login_at ? timeAgo(u.last_login_at) : 'never'}
            </Text>
            {canManage && (
              <Group gap="xs" mt="xs">
                <Button size="xs" variant="default" leftSection={<IconKey size={14} />} loading={m.resetLink.isPending}
                  onClick={() =>
                    m.resetLink.mutate(id, {
                      onSuccess: (l) => setLink({ link: l, title: `Password link for ${u.display_name}` }),
                      onError: notifyErr,
                    })
                  }>
                  {u.has_password ? 'Password reset link' : 'Set-password link'}
                </Button>
                <Button size="xs" variant="default" leftSection={<IconLogout size={14} />}
                  onClick={() => m.revokeSessions.mutate(id, { onSuccess: () => notifyOk('Signed out everywhere'), onError: notifyErr })}>
                  Sign out everywhere
                </Button>
                <Button size="xs" variant="default"
                  onClick={() => m.clearThrottle.mutate(id, { onSuccess: () => notifyOk('Sign-in throttling cleared'), onError: notifyErr })}>
                  Clear sign-in throttling
                </Button>
                {!self && (
                  <Button size="xs" variant="subtle" color="red" leftSection={<IconTrash size={14} />} onClick={() => setDeleting(true)}>
                    Delete
                  </Button>
                )}
              </Group>
            )}
          </Stack>
        </Card>
      </SimpleGrid>
      <UserSessions id={id} />
      <UserIdentities id={id} canManage={canManage} onLink={(l) => setLink({ link: l, title: `SSO link invitation for ${u.display_name}` })} />
      <UserTokens id={id} canManage={canManage} />
      <OneTimeLinkModal link={link?.link ?? null} title={link?.title ?? ''} onClose={() => setLink(null)} />
      <ConfirmModal
        opened={deleting}
        onClose={() => setDeleting(false)}
        title={`Delete ${u.display_name}`}
        confirmText={u.email ?? u.display_name}
        loading={m.remove.isPending}
        onConfirm={() =>
          m.remove.mutate(id, {
            onSuccess: () => {
              notifyOk('User deleted')
              void navigate({ to: '/users' })
            },
            onError: notifyErr,
          })
        }
      >
        <Text size="sm">Their sessions, linked identities and personal access tokens are removed. The audit log keeps their actions.</Text>
      </ConfirmModal>
    </>
  )
}

function UserSessions({ id }: { id: string }) {
  const sessions = useUserSessions(id)
  return (
    <Card mb="sm">
      <Title order={5} mb="xs">
        Active sessions
      </Title>
      <ErrorAlert error={sessions.error} />
      {(sessions.data ?? []).length === 0 ? (
        <Text size="sm" c="dimmed">
          None.
        </Text>
      ) : (
        <Table striped>
          <Table.Thead>
            <Table.Tr>
              <Table.Th>Signed in</Table.Th>
              <Table.Th>Method</Table.Th>
              <Table.Th>Where</Table.Th>
              <Table.Th>Address</Table.Th>
              <Table.Th>Last active</Table.Th>
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {sessions.data!.map((s) => (
              <Table.Tr key={s.id}>
                <Table.Td>{formatTime(s.created_at)}</Table.Td>
                <Table.Td>{s.auth_provider ? `sso (${s.auth_provider})` : s.auth_method}</Table.Td>
                <Table.Td>{s.listener === 'public' ? 'dashboard host' : 'ssh tunnel'}</Table.Td>
                <Table.Td>
                  <Code>{s.ip}</Code>
                </Table.Td>
                <Table.Td>{timeAgo(s.last_seen_at)}</Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      )}
    </Card>
  )
}

function UserIdentities({ id, canManage, onLink }: { id: string; canManage: boolean; onLink: (l: OneTimeLink) => void }) {
  const ids = useUserIdentities(id)
  const canSSO = useCan('auth.settings')
  const providers = useSSOProviders(canSSO)
  const m = useUserMutations()
  const [linking, setLinking] = useState(false)
  const [provider, setProvider] = useState<string | null>(null)
  const [subject, setSubject] = useState('')
  const options = (providers.data ?? []).map((p) => ({ value: p.id, label: p.display_name }))
  return (
    <Card mb="sm">
      <Group justify="space-between" mb="xs">
        <Title order={5}>Single sign-on identities</Title>
        {canManage && canSSO && options.length > 0 && (
          <Group gap="xs">
            <Select size="xs" placeholder="Provider" data={options} value={provider} onChange={setProvider} w={180} />
            <Button size="xs" variant="default" disabled={!provider} onClick={() => setLinking(true)}>
              Link by subject
            </Button>
            <Button size="xs" variant="default" leftSection={<IconLink size={14} />} disabled={!provider}
              onClick={() => provider && m.linkInvite.mutate({ id, provider_id: provider }, { onSuccess: onLink, onError: notifyErr })}>
              Link invitation
            </Button>
          </Group>
        )}
      </Group>
      <ErrorAlert error={ids.error} />
      {(ids.data ?? []).length === 0 ? (
        <Text size="sm" c="dimmed">
          No identity linked. Link one by its subject, or send a link invitation: the next SSO sign-in through it binds to this user.
        </Text>
      ) : (
        <Table striped>
          <Table.Thead>
            <Table.Tr>
              <Table.Th>Issuer</Table.Th>
              <Table.Th>Subject</Table.Th>
              <Table.Th>Linked</Table.Th>
              <Table.Th>Last sign-in</Table.Th>
              <Table.Th />
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {ids.data!.map((i) => (
              <Table.Tr key={i.issuer + i.subject}>
                <Table.Td>{i.issuer}</Table.Td>
                <Table.Td>
                  <Code>{i.subject}</Code>
                </Table.Td>
                <Table.Td>
                  {formatTime(i.created_at)} ({i.linked_via})
                </Table.Td>
                <Table.Td>{i.last_login_at ? timeAgo(i.last_login_at) : 'never'}</Table.Td>
                <Table.Td>
                  {canManage && (
                    <Button size="compact-xs" variant="subtle" color="red"
                      onClick={() =>
                        m.unlinkIdentity.mutate(
                          { id, issuer: i.issuer, subject: i.subject },
                          { onSuccess: () => { notifyOk('Identity unlinked'); void ids.refetch() }, onError: notifyErr },
                        )
                      }>
                      Unlink
                    </Button>
                  )}
                </Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      )}
      <Modal opened={linking} onClose={() => setLinking(false)} title="Link an SSO identity">
        <form
          onSubmit={(e) => {
            e.preventDefault()
            if (!provider) return
            m.linkIdentity.mutate(
              { id, provider_id: provider, subject: subject.trim() },
              {
                onSuccess: () => {
                  setLinking(false)
                  setSubject('')
                  notifyOk('Identity linked')
                  void ids.refetch()
                },
                onError: notifyErr,
              },
            )
          }}
        >
          <Stack gap="sm">
            <TextInput label="Subject (sub claim)" required value={subject} onChange={(e) => setSubject(e.currentTarget.value)} data-autofocus
              description="The identity provider's stable user ID. Email addresses are never used to match accounts." />
            <Group justify="flex-end">
              <Button variant="default" onClick={() => setLinking(false)}>
                Cancel
              </Button>
              <Button type="submit" loading={m.linkIdentity.isPending}>
                Link
              </Button>
            </Group>
          </Stack>
        </form>
      </Modal>
    </Card>
  )
}

function UserTokens({ id, canManage }: { id: string; canManage: boolean }) {
  const tokens = useUserTokens(id)
  const m = useUserMutations()
  if ((tokens.data ?? []).length === 0) return null
  return (
    <Card mb="sm">
      <Title order={5} mb="xs">
        Personal access tokens
      </Title>
      <Table striped>
        <Table.Thead>
          <Table.Tr>
            <Table.Th>Name</Table.Th>
            <Table.Th>Role</Table.Th>
            <Table.Th>Networks</Table.Th>
            <Table.Th>Expires</Table.Th>
            <Table.Th>Last used</Table.Th>
            <Table.Th />
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {tokens.data!.map((t) => (
            <Table.Tr key={t.id}>
              <Table.Td>
                {t.name} <Code>pulse_pat_{t.prefix}_…</Code>
              </Table.Td>
              <Table.Td>
                <RoleBadges roles={[t.role]} />
              </Table.Td>
              <Table.Td ff="monospace">{t.cidrs.join(', ') || 'loopback only'}</Table.Td>
              <Table.Td>{formatTime(t.expires_at)}</Table.Td>
              <Table.Td>{t.last_used_at ? `${timeAgo(t.last_used_at)} from ${t.last_used_ip}` : 'never'}</Table.Td>
              <Table.Td>
                {t.revoked ? (
                  <Badge size="sm" color="gray" variant="light">
                    revoked
                  </Badge>
                ) : (
                  canManage && (
                    <Button size="compact-xs" variant="subtle" color="red"
                      onClick={() => m.revokeToken.mutate({ id, token: t.id }, { onSuccess: () => { notifyOk('Token revoked'); void tokens.refetch() }, onError: notifyErr })}>
                      Revoke
                    </Button>
                  )
                )}
              </Table.Td>
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
    </Card>
  )
}
