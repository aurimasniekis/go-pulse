import {
  Alert, Badge, Button, Card, Checkbox, Code, CopyButton, Group, Modal, PasswordInput, Select, SimpleGrid, Stack, Switch, Table, TagsInput, Text,
  Textarea, TextInput, Title,
} from '@mantine/core'
import { IconCheck, IconCopy, IconPlus, IconTestPipe, IconTrash } from '@tabler/icons-react'
import { useState } from 'react'
import { ROLES, useAuthConfig } from '@/api/auth'
import type { ProviderInput, SSOProvider } from '@/api/client'
import { useSSOMappings, useSSOMutations, useSSOProviders, useSSOSettings, useSSOTestResult } from '@/api/users'
import { ConfirmModal } from '@/components/ConfirmModal'
import { ErrorAlert } from '@/components/ErrorAlert'
import { RoleBadges } from '@/components/RoleBadges'
import { oidcStartUrl } from '@/features/auth/sso'
import { formatTime } from '@/lib/format'
import { notifyErr, notifyOk } from '@/lib/notify'

/**
 * Single sign-on settings (ADR-0038): OpenID Connect providers, group → role mappings, test logins and SSO-only
 * enforcement. Provider changes, mappings and SSO-only need a recent sign-in (the re-authentication prompt handles it).
 */
export function SSOSettings({ testLogin }: { testLogin?: string }) {
  const providers = useSSOProviders()
  const settings = useSSOSettings()
  const config = useAuthConfig()
  const m = useSSOMutations()
  const [editing, setEditing] = useState<SSOProvider | 'new' | null>(null)
  const [mappings, setMappings] = useState<SSOProvider | null>(null)
  const [deleting, setDeleting] = useState<SSOProvider | null>(null)
  const ssoOnly = settings.data?.local_login_disabled ?? false
  const ssoHere = config.data?.sso_here ?? false
  const callback = `${ssoHere ? window.location.origin : (config.data?.sso_url ?? window.location.origin)}/auth/oidc/callback`

  return (
    <Stack gap="sm">
      {testLogin && <TestResultCard providerId={testLogin} providers={providers.data ?? []} />}
      <Card>
        <Group justify="space-between" mb="xs">
          <Title order={5}>Identity providers</Title>
          <Button size="xs" leftSection={<IconPlus size={14} />} onClick={() => setEditing('new')}>
            Add provider
          </Button>
        </Group>
        <Text size="sm" c="dimmed" mb="xs">
          Any OpenID Connect provider (Keycloak, Authentik, Entra ID, Google, Okta, Dex…). Register this redirect URI with the provider:{' '}
          <Code>{callback}</Code>
          {!ssoHere && ' Single sign-on runs on that origin, so test logins start there.'}
        </Text>
        <ErrorAlert error={providers.error} />
        {(providers.data ?? []).length === 0 ? (
          <Text size="sm" c="dimmed">
            No provider yet.
          </Text>
        ) : (
          <Table striped data-testid="sso-providers">
            <Table.Thead>
              <Table.Tr>
                <Table.Th>Provider</Table.Th>
                <Table.Th>Issuer</Table.Th>
                <Table.Th>Accounts</Table.Th>
                <Table.Th>Test login</Table.Th>
                <Table.Th>Status</Table.Th>
                <Table.Th />
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {providers.data!.map((p) => (
                <Table.Tr key={p.id}>
                  <Table.Td>
                    {p.display_name} <Code>{p.slug}</Code>
                  </Table.Td>
                  <Table.Td>
                    <Text size="xs" style={{ wordBreak: 'break-all' }}>
                      {p.issuer}
                    </Text>
                  </Table.Td>
                  <Table.Td>{p.provisioning === 'jit' ? 'created at first sign-in' : 'linked by an administrator'}</Table.Td>
                  <Table.Td>{p.test_login_ok_at ? formatTime(p.test_login_ok_at) : 'never'}</Table.Td>
                  <Table.Td>
                    <Badge size="sm" variant="dot" color={p.enabled ? 'teal' : 'gray'}>
                      {p.enabled ? 'enabled' : 'disabled'}
                    </Badge>
                  </Table.Td>
                  <Table.Td>
                    <Group gap={4} justify="flex-end" wrap="nowrap">
                      <Button size="compact-xs" variant="subtle" onClick={() => setEditing(p)}>
                        Edit
                      </Button>
                      <Button size="compact-xs" variant="subtle" onClick={() => setMappings(p)}>
                        Mappings
                      </Button>
                      {ssoHere && (
                        <Button size="compact-xs" variant="subtle" component="a" leftSection={<IconTestPipe size={12} />}
                          href={oidcStartUrl(p.slug, 'test', '/settings?tab=sso')}>
                          Test login
                        </Button>
                      )}
                      <Button size="compact-xs" variant="subtle" color="red" onClick={() => setDeleting(p)} aria-label={`Delete ${p.slug}`}>
                        <IconTrash size={12} />
                      </Button>
                    </Group>
                  </Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        )}
      </Card>
      <Card>
        <Title order={5} mb="xs">
          SSO-only
        </Title>
        <Switch
          checked={ssoOnly}
          label="Disable password sign-in"
          description="Allowed once an administrator completed a test login on an enabled provider and an administrator can sign in with SSO. Break-glass recovery (pulsed admin recover) keeps working."
          disabled={m.setLocalLoginDisabled.isPending}
          onChange={(e) =>
            m.setLocalLoginDisabled.mutate(e.currentTarget.checked, {
              onSuccess: (r) => notifyOk(r.local_login_disabled ? 'Password sign-in disabled' : 'Password sign-in enabled'),
              onError: notifyErr,
            })
          }
        />
      </Card>
      <ProviderModal provider={editing} callback={callback} onClose={() => setEditing(null)} />
      <MappingsModal provider={mappings} onClose={() => setMappings(null)} />
      <ConfirmModal
        opened={deleting !== null}
        onClose={() => setDeleting(null)}
        title={`Delete ${deleting?.display_name ?? ''}`}
        confirmText={deleting?.slug ?? ''}
        loading={m.remove.isPending}
        onConfirm={() =>
          deleting &&
          m.remove.mutate(deleting.id, {
            onSuccess: () => {
              notifyOk('Provider deleted')
              setDeleting(null)
            },
            onError: notifyErr,
          })
        }
      >
        <Text size="sm">Linked identities and mappings are removed; their users lose SSO-mapped roles and are signed out.</Text>
      </ConfirmModal>
    </Stack>
  )
}

interface Draft {
  display_name: string
  slug: string
  issuer: string
  client_id: string
  client_secret: string
  clear_secret: boolean
  scopes: string[]
  groups_claim: string
  provisioning: 'preprovisioned' | 'jit'
  email_domains: string[]
  tenants: string[]
  hosted_domains: string[]
  groups: string[]
  stepup_fallback: 'none' | 'local_totp'
  acr_values: string[]
  ca_bundle_pem: string
  allow_private_network: boolean
  allow_cross_origin_endpoints: boolean
  end_session_enabled: boolean
  enabled: boolean
}

function draftOf(p: SSOProvider | null): Draft {
  return {
    display_name: p?.display_name ?? '',
    slug: p?.slug ?? '',
    issuer: p?.issuer ?? '',
    client_id: p?.client_id ?? '',
    client_secret: '',
    clear_secret: false,
    scopes: p?.scopes ?? ['openid', 'profile', 'email'],
    groups_claim: p?.groups_claim ?? 'groups',
    provisioning: p?.provisioning === 'jit' ? 'jit' : 'preprovisioned',
    email_domains: p?.jit.email_domains ?? [],
    tenants: p?.jit.tenants ?? [],
    hosted_domains: p?.jit.hosted_domains ?? [],
    groups: p?.jit.groups ?? [],
    stepup_fallback: p?.stepup_fallback === 'local_totp' ? 'local_totp' : 'none',
    acr_values: p?.acr_values ?? [],
    ca_bundle_pem: p?.ca_bundle_pem ?? '',
    allow_private_network: p?.allow_private_network ?? false,
    allow_cross_origin_endpoints: p?.allow_cross_origin_endpoints ?? false,
    end_session_enabled: p?.end_session_enabled ?? false,
    enabled: p?.enabled ?? true,
  }
}

/** The PUT/POST body; the secret is sent only when typed (or explicitly cleared). */
export function providerBody(d: Draft, version?: number): ProviderInput {
  const body: ProviderInput = {
    display_name: d.display_name.trim(),
    slug: d.slug.trim(),
    issuer: d.issuer.trim(),
    client_id: d.client_id.trim(),
    scopes: d.scopes,
    groups_claim: d.groups_claim.trim(),
    provisioning: d.provisioning,
    jit: { email_domains: d.email_domains, tenants: d.tenants, hosted_domains: d.hosted_domains, groups: d.groups },
    stepup_fallback: d.stepup_fallback,
    acr_values: d.acr_values,
    ca_bundle_pem: d.ca_bundle_pem,
    allow_private_network: d.allow_private_network,
    allow_cross_origin_endpoints: d.allow_cross_origin_endpoints,
    end_session_enabled: d.end_session_enabled,
    enabled: d.enabled,
  }
  if (version !== undefined) body.version = version
  if (d.clear_secret) body.client_secret = ''
  else if (d.client_secret) body.client_secret = d.client_secret
  return body
}

function ProviderModal({ provider, callback, onClose }: { provider: SSOProvider | 'new' | null; callback: string; onClose: () => void }) {
  const editing = provider !== null && provider !== 'new' ? provider : null
  return (
    <Modal opened={provider !== null} onClose={onClose} title={editing ? `Edit ${editing.display_name}` : 'Add identity provider'} size="xl"
      closeOnClickOutside={false}>
      {provider !== null && <ProviderForm key={editing?.id ?? 'new'} provider={editing} callback={callback} onClose={onClose} />}
    </Modal>
  )
}

function ProviderForm({ provider, callback, onClose }: { provider: SSOProvider | null; callback: string; onClose: () => void }) {
  const m = useSSOMutations()
  const [d, setD] = useState<Draft>(draftOf(provider))
  const set = <K extends keyof Draft>(k: K, v: Draft[K]) => setD((x) => ({ ...x, [k]: v }))
  const pending = m.create.isPending || m.update.isPending
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault()
        const done = { onSuccess: () => { notifyOk('Provider saved'); onClose() }, onError: notifyErr }
        if (provider) m.update.mutate({ id: provider.id, body: providerBody(d, provider.version) }, done)
        else m.create.mutate(providerBody(d), done)
      }}
    >
      <Stack gap="sm">
        <Alert color="blue">
          Redirect URI to register: <Code>{provider?.redirect_uri ?? callback}</Code>
        </Alert>
        <SimpleGrid cols={{ base: 1, md: 2 }} spacing="sm">
          <TextInput label="Display name" required value={d.display_name} onChange={(e) => set('display_name', e.currentTarget.value)}
            description="Shown on the sign-in button" />
          <TextInput label="Slug" required value={d.slug} onChange={(e) => set('slug', e.currentTarget.value)}
            description="Lowercase letters, digits and dashes; part of the sign-in URL" />
          <TextInput label="Issuer" required value={d.issuer} onChange={(e) => set('issuer', e.currentTarget.value)}
            description="https://, exactly as in the provider's discovery document" />
          <TextInput label="Client ID" required value={d.client_id} onChange={(e) => set('client_id', e.currentTarget.value)} />
          <Stack gap={4}>
            <PasswordInput label="Client secret" value={d.client_secret} disabled={d.clear_secret} autoComplete="off"
              placeholder={provider?.has_client_secret ? 'unchanged (stored, write-only)' : 'none (public client with PKCE)'}
              onChange={(e) => set('client_secret', e.currentTarget.value)} />
            {provider?.has_client_secret && (
              <Checkbox size="xs" label="Remove the stored secret" checked={d.clear_secret} onChange={(e) => set('clear_secret', e.currentTarget.checked)} />
            )}
          </Stack>
          <TagsInput label="Scopes" value={d.scopes} onChange={(v) => set('scopes', v)} description="Must include openid" />
          <TextInput label="Groups claim" value={d.groups_claim} onChange={(e) => set('groups_claim', e.currentTarget.value)}
            description="Claim with group values for role mappings (e.g. groups)" />
          <Select label="Accounts" value={d.provisioning} allowDeselect={false} onChange={(v) => set('provisioning', (v ?? 'preprovisioned') as Draft['provisioning'])}
            data={[
              { value: 'preprovisioned', label: 'Linked by an administrator' },
              { value: 'jit', label: 'Created at first sign-in (restricted)' },
            ]} />
        </SimpleGrid>
        {d.provisioning === 'jit' && (
          <Card withBorder>
            <Text size="sm" fw={500} mb={4}>
              Who may get an account (every rule you set must match; at least one is required)
            </Text>
            <SimpleGrid cols={{ base: 1, md: 2 }} spacing="sm">
              <TagsInput label="Verified email domains" value={d.email_domains} onChange={(v) => set('email_domains', v)} />
              <TagsInput label="Groups" value={d.groups} onChange={(v) => set('groups', v)} />
              <TagsInput label="Tenants (tid)" value={d.tenants} onChange={(v) => set('tenants', v)} />
              <TagsInput label="Hosted domains (hd)" value={d.hosted_domains} onChange={(v) => set('hosted_domains', v)} />
            </SimpleGrid>
            <Text size="xs" c="dimmed" mt={4}>
              New accounts get no role except through group mappings.
            </Text>
          </Card>
        )}
        <SimpleGrid cols={{ base: 1, md: 2 }} spacing="sm">
          <Select label="Step-up fallback" value={d.stepup_fallback} allowDeselect={false}
            onChange={(v) => set('stepup_fallback', (v ?? 'none') as Draft['stepup_fallback'])}
            description="When the provider cannot prove a fresh sign-in (no auth_time)"
            data={[
              { value: 'none', label: 'None' },
              { value: 'local_totp', label: "The user's Pulse authenticator" },
            ]} />
          <TagsInput label="ACR values" value={d.acr_values} onChange={(v) => set('acr_values', v)} description="Requested, and required on step-up" />
        </SimpleGrid>
        <Textarea label="Extra CA certificates (PEM)" autosize minRows={2} maxRows={6} value={d.ca_bundle_pem}
          onChange={(e) => set('ca_bundle_pem', e.currentTarget.value)} description="For a provider with a private CA; TLS verification is never disabled" />
        <Group gap="lg">
          <Switch label="Enabled" checked={d.enabled} onChange={(e) => set('enabled', e.currentTarget.checked)} />
          <Switch label="Offer sign-out at the provider" checked={d.end_session_enabled} onChange={(e) => set('end_session_enabled', e.currentTarget.checked)} />
          <Switch label="Allow a private-network provider" checked={d.allow_private_network}
            onChange={(e) => set('allow_private_network', e.currentTarget.checked)} />
          <Switch label="Allow endpoints outside the issuer's site" checked={d.allow_cross_origin_endpoints}
            onChange={(e) => set('allow_cross_origin_endpoints', e.currentTarget.checked)} />
        </Group>
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" loading={pending}>
            Save
          </Button>
        </Group>
      </Stack>
    </form>
  )
}

function MappingsModal({ provider, onClose }: { provider: SSOProvider | null; onClose: () => void }) {
  const mappings = useSSOMappings(provider?.id ?? null)
  const m = useSSOMutations()
  const [group, setGroup] = useState('')
  const [role, setRole] = useState<string | null>('viewer')
  return (
    <Modal opened={provider !== null} onClose={onClose} title={`Group → role mappings: ${provider?.display_name ?? ''}`} size="lg">
      <Stack gap="sm">
        <Text size="sm" c="dimmed">
          At every sign-in through this provider, the user's SSO roles are replaced with the roles mapped from their groups (claim{' '}
          <Code>{provider?.groups_claim || '—'}</Code>). Roles set by an administrator are kept.
        </Text>
        <ErrorAlert error={mappings.error} />
        <Table>
          <Table.Tbody>
            {(mappings.data ?? []).map((mp) => (
              <Table.Tr key={mp.id}>
                <Table.Td>
                  <Code>{mp.group_value}</Code>
                </Table.Td>
                <Table.Td>
                  <RoleBadges roles={[mp.role]} />
                </Table.Td>
                <Table.Td ta="right">
                  <Button size="compact-xs" variant="subtle" color="red"
                    onClick={() => provider && m.deleteMapping.mutate({ id: provider.id, mapping: mp.id }, { onError: notifyErr })}>
                    Remove
                  </Button>
                </Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
        <form
          onSubmit={(e) => {
            e.preventDefault()
            if (!provider || !role) return
            m.addMapping.mutate({ id: provider.id, group_value: group.trim(), role }, { onSuccess: () => setGroup(''), onError: notifyErr })
          }}
        >
          <Group align="flex-end" gap="xs">
            <TextInput label="Group value" required value={group} onChange={(e) => setGroup(e.currentTarget.value)} style={{ flex: 1 }} />
            <Select label="Role" data={[...ROLES]} value={role} onChange={setRole} allowDeselect={false} w={160} />
            <Button type="submit" loading={m.addMapping.isPending}>
              Add
            </Button>
          </Group>
        </form>
      </Stack>
    </Modal>
  )
}

function TestResultCard({ providerId, providers }: { providerId: string; providers: SSOProvider[] }) {
  const res = useSSOTestResult(providerId)
  const name = providers.find((p) => p.id === providerId)?.display_name ?? providerId
  const r = res.data
  return (
    <Card withBorder style={{ borderColor: 'var(--mantine-color-teal-5)' }} data-testid="sso-test-result">
      <Title order={5} mb="xs">
        Test login: {name}
      </Title>
      <ErrorAlert error={res.error} />
      {r && (
        <Stack gap="xs">
          <Text size="sm">
            The provider's response was valid. Subject <Code>{r.claims.sub}</Code>
            {r.claims.email && (
              <>
                , email {r.claims.email} ({r.claims.email_verified ? 'verified' : 'not verified'})
              </>
            )}
            .
          </Text>
          <Text size="sm">
            {r.linked_user_id
              ? 'This identity is linked to a Pulse user.'
              : r.would_provision
                ? 'A new account would be created at sign-in.'
                : `No account would be used: ${r.provision_reason ?? 'not linked'}.`}
          </Text>
          <Group gap="xs">
            <Text size="sm">Groups:</Text>
            {(r.claims.groups ?? []).length === 0 ? <Text size="sm" c="dimmed">none</Text> : (r.claims.groups ?? []).map((g) => <Code key={g}>{g}</Code>)}
          </Group>
          <Group gap="xs">
            <Text size="sm">Roles from mappings:</Text>
            <RoleBadges roles={r.mapped_roles} />
            <Text size="sm">Effective roles:</Text>
            <RoleBadges roles={r.effective_roles} />
          </Group>
          <CopyButton value={JSON.stringify(r.claims, null, 2)}>
            {({ copied, copy }) => (
              <Button size="compact-xs" variant="subtle" onClick={copy} leftSection={copied ? <IconCheck size={12} /> : <IconCopy size={12} />} w="fit-content">
                Copy claims
              </Button>
            )}
          </CopyButton>
        </Stack>
      )}
    </Card>
  )
}
