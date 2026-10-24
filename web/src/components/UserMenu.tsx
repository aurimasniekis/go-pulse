import { Avatar, Badge, Button, Group, Menu, Modal, Stack, Text, UnstyledButton } from '@mantine/core'
import { useState } from 'react'
import { IconLogout, IconUser } from '@tabler/icons-react'
import { Link } from '@tanstack/react-router'
import { useMe } from '@/api/auth'
import { admin } from '@/api/client'
import { setCsrfToken } from '@/api/session'

function initials(name: string): string {
  return name.split(/\s+/).filter(Boolean).slice(0, 2).map((p) => p[0]?.toUpperCase()).join('') || '?'
}

/** Header user menu: who is signed in, Account, Sign out (with optional sign-out at the identity provider). */
export function UserMenu() {
  const me = useMe().data
  const [providerLogout, setProviderLogout] = useState<string | null>(null)
  if (!me) return null
  const signOut = async () => {
    const res = await admin.POST('/auth/logout').catch(() => null)
    setCsrfToken('')
    const url = res?.data?.provider_logout_url
    if (url) setProviderLogout(url)
    else window.location.assign('/login')
  }
  return (
    <>
    <Modal opened={providerLogout !== null} onClose={() => window.location.assign('/login')} title="Signed out of Pulse">
      <Stack gap="sm">
        <Text size="sm">Also sign out at your identity provider? Other applications that use it are signed out too.</Text>
        <Group justify="flex-end">
          <Button variant="default" onClick={() => window.location.assign('/login')}>
            Not now
          </Button>
          <Button onClick={() => providerLogout && window.location.assign(providerLogout)}>Sign out there too</Button>
        </Group>
      </Stack>
    </Modal>
    <Menu position="bottom-end" width={240} withinPortal>
      <Menu.Target>
        <UnstyledButton aria-label="User menu" data-testid="user-menu">
          <Avatar size={24} radius="xl" color="indigo">
            {initials(me.user.display_name)}
          </Avatar>
        </UnstyledButton>
      </Menu.Target>
      <Menu.Dropdown>
        <Menu.Label>
          <Text size="sm" fw={500} c="var(--mantine-color-text)">
            {me.user.display_name}
          </Text>
          {me.user.email && <Text size="xs">{me.user.email}</Text>}
          <Group gap={4} mt={4}>
            {me.user.roles.length === 0 ? (
              <Badge size="xs" color="gray" variant="light">no role</Badge>
            ) : (
              me.user.roles.map((r) => (
                <Badge key={r} size="xs" variant="light">
                  {r}
                </Badge>
              ))
            )}
            {me.auth_method === 'oidc' && <Badge size="xs" color="teal" variant="light">SSO</Badge>}
          </Group>
        </Menu.Label>
        <Menu.Divider />
        <Menu.Item component={Link} to="/account" leftSection={<IconUser size={14} />}>
          Account
        </Menu.Item>
        <Menu.Item leftSection={<IconLogout size={14} />} onClick={() => void signOut()}>
          Sign out
        </Menu.Item>
      </Menu.Dropdown>
    </Menu>
    </>
  )
}
