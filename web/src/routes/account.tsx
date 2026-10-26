import {
  Alert, Badge, Button, Card, Code, Group, Modal, NumberInput, PasswordInput, Select, SimpleGrid, Stack, Table, TagsInput, Text, TextInput, Title,
} from '@mantine/core'
import { IconKey, IconLink, IconLogout, IconPlus, IconShieldCheck, IconTrash } from '@tabler/icons-react'
import { useQueryClient } from '@tanstack/react-query'
import { createFileRoute } from '@tanstack/react-router'
import { useState } from 'react'
import { meKey, roleInfo, useAuthConfig, useMe, type Role } from '@/api/auth'
import { RoleBadges } from '@/components/RoleBadges'
import { useAccountMutations, useMyIdentities, useMySessions, useMyTokens } from '@/api/users'
import { ErrorAlert } from '@/components/ErrorAlert'
import { PageHeader } from '@/components/PageHeader'
import { SecretModal } from '@/components/SecretModal'
import { errorMessage } from '@/features/auth/errors'
import { newPasswordError } from '@/features/auth/password'
import { NewPasswordFields } from '@/features/auth/NewPasswordFields'
import { oidcStartUrl } from '@/features/auth/sso'
import { TotpEnrollment } from '@/features/auth/TotpEnrollment'
import { formatTime, timeAgo } from '@/lib/format'
import { notifyErr, notifyOk } from '@/lib/notify'

export const Route = createFileRoute('/account')({ component: Account })

function Account() {
  const me = useMe().data
  if (!me) return null
  const u = me.user
  return (
    <>
      <PageHeader title="Account" description="Your profile, sign-in methods, sessions and personal access tokens." />
      <SimpleGrid cols={{ base: 1, lg: 2 }} spacing="sm" mb="sm">
        <Card>
          <Title order={5} mb="xs">
            Profile
          </Title>
          <Table variant="vertical" withTableBorder={false}>
            <Table.Tbody>
              <Table.Tr>
                <Table.Th w={140}>Name</Table.Th>
                <Table.Td>{u.display_name}</Table.Td>
              </Table.Tr>
              <Table.Tr>
                <Table.Th>Email</Table.Th>
                <Table.Td>{u.email ?? '—'}</Table.Td>
              </Table.Tr>
              <Table.Tr>
                <Table.Th>Roles</Table.Th>
                <Table.Td>
                  <RoleBadges roles={u.roles} />
                </Table.Td>
              </Table.Tr>
              <Table.Tr>
                <Table.Th>This session</Table.Th>
                <Table.Td>
                  {me.auth_method === 'oidc' ? `single sign-on (${me.auth_provider})` : me.auth_method} · {me.listener} · until{' '}
                  {formatTime(me.expires_at)}
                </Table.Td>
              </Table.Tr>
            </Table.Tbody>
          </Table>
        </Card>
        <TwoFactorCard />
      </SimpleGrid>
      {(u.has_password || me.auth_method === 'password') && <PasswordCard />}
      <SessionsCard />
      <IdentitiesCard />
      <TokensCard />
    </>
  )
}

function TwoFactorCard() {
  const me = useMe().data!
  const qc = useQueryClient()
  const m = useAccountMutations()
  const [enrolling, setEnrolling] = useState(false)
  const [codes, setCodes] = useState<string[] | null>(null)
  const u = me.user
  return (
    <Card>
      <Group justify="space-between" mb="xs">
        <Title order={5}>Two-factor authentication</Title>
        <Badge color={u.mfa_enrolled ? 'teal' : u.mfa_required ? 'red' : 'gray'} variant="light" leftSection={<IconShieldCheck size={12} />}>
          {u.mfa_enrolled ? 'authenticator app' : u.mfa_required ? 'required, not set up' : 'off'}
        </Badge>
      </Group>
      <Stack gap="xs">
        <Text size="sm" c="dimmed">
          {u.mfa_enrolled
            ? `Sign-in with a password asks for a code from your authenticator app. ${me.recovery_codes_left} unused recovery codes left.`
            : 'Add an authenticator app (TOTP) for password sign-in and for confirming sensitive actions.'}
          {me.auth_method === 'oidc' && ' Single sign-on sessions use your identity provider; this factor is the step-up fallback when the provider allows it.'}
        </Text>
        <Group gap="xs">
          <Button size="xs" onClick={() => setEnrolling(true)}>
            {u.mfa_enrolled ? 'Replace authenticator' : 'Set up authenticator'}
          </Button>
          {u.mfa_enrolled && (
            <Button size="xs" variant="default" loading={m.regenerateCodes.isPending}
              onClick={() => m.regenerateCodes.mutate(undefined, { onSuccess: setCodes, onError: notifyErr })}>
              New recovery codes
            </Button>
          )}
          {u.mfa_enrolled && !u.mfa_required && (
            <Button size="xs" variant="subtle" color="red" loading={m.disableTotp.isPending}
              onClick={() => m.disableTotp.mutate(undefined, { onSuccess: () => notifyOk('Authenticator removed'), onError: notifyErr })}>
              Remove
            </Button>
          )}
        </Group>
      </Stack>
      <Modal opened={enrolling} onClose={() => setEnrolling(false)} title="Set up an authenticator" size="md" closeOnClickOutside={false}>
        {enrolling && (
          <TotpEnrollment
            onCancel={() => setEnrolling(false)}
            onDone={() => {
              setEnrolling(false)
              notifyOk('Authenticator set up')
              void qc.invalidateQueries({ queryKey: meKey })
            }}
          />
        )}
      </Modal>
      <SecretModal secret={codes ? codes.join('\n') : null} title="Recovery codes" filename="pulse-recovery-codes.txt" onClose={() => setCodes(null)}>
        <Text size="sm" c="dimmed">
          The previous codes no longer work. Each code signs you in once if you lose your authenticator.
        </Text>
      </SecretModal>
    </Card>
  )
}

function PasswordCard() {
  const m = useAccountMutations()
  const [current, setCurrent] = useState('')
  const [pw, setPw] = useState({ password: '', confirm: '' })
  const [error, setError] = useState<unknown>(null)
  return (
    <Card mb="sm">
      <Title order={5} mb="xs">
        Password
      </Title>
      <form
        aria-label="Change password"
        onSubmit={(e) => {
          e.preventDefault()
          const err = newPasswordError(pw.password, pw.confirm)
          if (err) return setError(new Error(err))
          setError(null)
          m.changePassword.mutate(
            { current_password: current, new_password: pw.password },
            {
              onSuccess: () => {
                notifyOk('Password changed; your other sessions were signed out')
                setCurrent('')
                setPw({ password: '', confirm: '' })
              },
              onError: setError,
            },
          )
        }}
      >
        <SimpleGrid cols={{ base: 1, md: 3 }} spacing="sm">
          <PasswordInput label="Current password" autoComplete="current-password" required value={current}
            onChange={(e) => setCurrent(e.currentTarget.value)} />
          <NewPasswordFields label="New password" password={pw.password} confirm={pw.confirm}
            onChange={(password, confirm) => setPw({ password, confirm })} />
        </SimpleGrid>
        {error !== null && (
          <Alert color="red" mt="sm">
            {errorMessage(error)}
          </Alert>
        )}
        <Group justify="flex-end" mt="sm">
          <Button type="submit" size="xs" loading={m.changePassword.isPending}>
            Change password
          </Button>
        </Group>
      </form>
    </Card>
  )
}

function SessionsCard() {
  const sessions = useMySessions()
  const m = useAccountMutations()
  return (
    <Card mb="sm">
      <Title order={5} mb="xs">
        Sessions
      </Title>
      <ErrorAlert error={sessions.error} />
      <Table striped highlightOnHover>
        <Table.Thead>
          <Table.Tr>
            <Table.Th>Signed in</Table.Th>
            <Table.Th>Method</Table.Th>
            <Table.Th>Where</Table.Th>
            <Table.Th>Address</Table.Th>
            <Table.Th>Browser</Table.Th>
            <Table.Th>Last active</Table.Th>
            <Table.Th />
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {(sessions.data ?? []).map((s) => (
            <Table.Tr key={s.id}>
              <Table.Td>{formatTime(s.created_at)}</Table.Td>
              <Table.Td>{s.auth_provider ? `sso (${s.auth_provider})` : s.auth_method}</Table.Td>
              <Table.Td>{s.listener === 'public' ? 'dashboard host' : 'ssh tunnel'}</Table.Td>
              <Table.Td>
                <Code>{s.ip}</Code>
              </Table.Td>
              <Table.Td>
                <Text size="xs" lineClamp={1} maw={260} title={s.user_agent}>
                  {s.user_agent}
                </Text>
              </Table.Td>
              <Table.Td>{timeAgo(s.last_seen_at)}</Table.Td>
              <Table.Td>
                {s.current ? (
                  <Badge size="sm" variant="light">
                    this session
                  </Badge>
                ) : (
                  <Button size="compact-xs" variant="subtle" color="red" leftSection={<IconLogout size={12} />}
                    onClick={() => m.revokeSession.mutate(s.id, { onSuccess: () => notifyOk('Signed out'), onError: notifyErr })}>
                    Sign out
                  </Button>
                )}
              </Table.Td>
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
    </Card>
  )
}

function IdentitiesCard() {
  const ids = useMyIdentities()
  const config = useAuthConfig()
  const providers = config.data?.providers ?? []
  if (providers.length === 0 && (ids.data ?? []).length === 0) return null
  return (
    <Card mb="sm">
      <Title order={5} mb="xs">
        Single sign-on identities
      </Title>
      <Stack gap="xs">
        {(ids.data ?? []).length === 0 ? (
          <Text size="sm" c="dimmed">
            No identity is linked. Link one to sign in with your identity provider.
          </Text>
        ) : (
          <Table>
            <Table.Thead>
              <Table.Tr>
                <Table.Th>Issuer</Table.Th>
                <Table.Th>Subject</Table.Th>
                <Table.Th>Linked</Table.Th>
                <Table.Th>Last sign-in</Table.Th>
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {ids.data!.map((i) => (
                <Table.Tr key={i.issuer + i.subject}>
                  <Table.Td>{i.issuer}</Table.Td>
                  <Table.Td>
                    <Code>{i.subject}</Code>
                  </Table.Td>
                  <Table.Td>{formatTime(i.created_at)} ({i.linked_via})</Table.Td>
                  <Table.Td>{i.last_login_at ? timeAgo(i.last_login_at) : 'never'}</Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        )}
        {providers.length > 0 &&
          (config.data?.sso_here ? (
            <Group gap="xs">
              {providers.map((p) => (
                <Button key={p.slug} size="xs" variant="default" leftSection={<IconLink size={14} />} component="a"
                  href={oidcStartUrl(p.slug, 'link', '/account')}>
                  Link {p.display_name}
                </Button>
              ))}
            </Group>
          ) : (
            <Text size="xs" c="dimmed">
              Link identities on the dashboard host {config.data?.sso_url}, where single sign-on runs.
            </Text>
          ))}
      </Stack>
    </Card>
  )
}

function TokensCard() {
  const me = useMe().data!
  const tokens = useMyTokens()
  const m = useAccountMutations()
  const manual = [...new Set(me.user.role_assignments.filter((a) => a.source === 'manual').map((a) => a.role))]
  const [creating, setCreating] = useState(false)
  const [name, setName] = useState('')
  const [role, setRole] = useState<string | null>(null)
  const [days, setDays] = useState<number | string>(90)
  const [cidrs, setCidrs] = useState<string[]>([])
  const [secret, setSecret] = useState<string | null>(null)
  return (
    <Card mb="sm">
      <Group justify="space-between" mb="xs">
        <Title order={5}>Personal access tokens</Title>
        <Button size="xs" leftSection={<IconPlus size={14} />} disabled={manual.length === 0 || me.auth_method === 'token'}
          onClick={() => {
            setName('')
            setCidrs([])
            setRole(manual[manual.length - 1] ?? null)
            setCreating(true)
          }}>
          New token
        </Button>
      </Group>
      <Text size="sm" c="dimmed" mb="xs">
        For the <Code>pulse</Code> CLI's admin commands (<Code>clients</Code>, <Code>streams</Code>, <Code>backup</Code>) and automation such as
        Terraform. Tokens work on the loopback admin listener, through the SSH tunnel, and carry one of your own roles. A token with
        networks also works on the public dashboard host from those networks, if the server allows it.
        {manual.length === 0 && ' You have no manually assigned role, so you cannot create one.'}
      </Text>
      <ErrorAlert error={tokens.error} />
      {(tokens.data ?? []).length > 0 && (
        <Table striped>
          <Table.Thead>
            <Table.Tr>
              <Table.Th>Name</Table.Th>
              <Table.Th>Prefix</Table.Th>
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
                <Table.Td>{t.name}</Table.Td>
                <Table.Td>
                  <Code>pulse_pat_{t.prefix}_…</Code>
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
                    <Button size="compact-xs" variant="subtle" color="red" leftSection={<IconTrash size={12} />}
                      onClick={() => m.revokeToken.mutate(t.id, { onSuccess: () => notifyOk('Token revoked'), onError: notifyErr })}>
                      Revoke
                    </Button>
                  )}
                </Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      )}
      <Modal opened={creating} onClose={() => setCreating(false)} title="New personal access token">
        <form
          onSubmit={(e) => {
            e.preventDefault()
            if (!role) return
            m.createToken.mutate(
              { name: name.trim(), role: role, days: typeof days === 'number' ? days : undefined, cidrs },
              {
                onSuccess: (r) => {
                  setCreating(false)
                  setSecret(r.secret)
                },
                onError: notifyErr,
              },
            )
          }}
        >
          <Stack gap="sm">
            <TextInput label="Name" placeholder="laptop cli" required value={name} onChange={(e) => setName(e.currentTarget.value)} data-autofocus />
            <Select label="Role" data={manual} value={role} onChange={setRole} allowDeselect={false}
              description={role ? roleInfo[role as Role]?.description : undefined} />
            <NumberInput label="Lifetime (days)" min={1} max={366} value={days} onChange={setDays} />
            <TagsInput label="Public networks" placeholder="203.0.113.7"
              description="IPs or CIDRs (at least /16 or /48) that may also use the token on the public dashboard host; empty keeps it loopback-only"
              value={cidrs} onChange={setCidrs} />
            <Group justify="flex-end">
              <Button variant="default" onClick={() => setCreating(false)}>
                Cancel
              </Button>
              <Button type="submit" leftSection={<IconKey size={14} />} loading={m.createToken.isPending}>
                Create
              </Button>
            </Group>
          </Stack>
        </form>
      </Modal>
      <SecretModal secret={secret} title="Personal access token" filename="pulse-admin-token" onClose={() => setSecret(null)}>
        <Text size="sm" c="dimmed">
          Save it to a file (mode 0600) and use it with <Code>pulse context add … --admin-token-file &lt;file&gt;</Code> or
          <Code>PULSE_ADMIN_TOKEN</Code>.
        </Text>
      </SecretModal>
    </Card>
  )
}
