import { ActionIcon, AppShell, Group, Kbd, NavLink, ScrollArea, Stack, Text, Tooltip, UnstyledButton, useComputedColorScheme, useMantineColorScheme } from '@mantine/core'
import { useDisclosure, useLocalStorage } from '@mantine/hooks'
import { spotlight } from '@mantine/spotlight'
import { IconLayoutSidebarLeftCollapse, IconLayoutSidebarLeftExpand, IconMoon, IconSearch, IconSun } from '@tabler/icons-react'
import { Link, useRouterState } from '@tanstack/react-router'
import type { ReactNode } from 'react'
import { useMe } from '@/api/auth'
import { brandIcon as Brand, nav, visibleNav } from '@/lib/nav'
import { CommandPalette } from './CommandPalette'
import { NoAccess } from './NoAccess'
import { StatusIndicator } from './StatusIndicator'
import { UserMenu } from './UserMenu'

export function AppLayout({ children }: { children: ReactNode }) {
  const [collapsed, setCollapsed] = useLocalStorage({ key: 'pulse.sidebar.collapsed', defaultValue: false })
  const [mobileOpen, { toggle: toggleMobile }] = useDisclosure(false)
  const { setColorScheme } = useMantineColorScheme()
  const scheme = useComputedColorScheme('light')
  const path = useRouterState({ select: (s) => s.location.pathname })
  const me = useMe().data
  const perms = me?.permissions ?? []
  const items = visibleNav(perms)
  const page = nav.find((n) => (n.to === '/' ? path === '/' : path.startsWith(n.to)))
  const allowed = !page || perms.includes(page.perm)
  const width = collapsed ? 52 : 210
  const groups = ['Events', 'Access', 'Operations'] as const

  return (
    <AppShell
      header={{ height: 40 }}
      navbar={{ width, breakpoint: 'sm', collapsed: { mobile: !mobileOpen } }}
      padding="md"
    >
      <AppShell.Header px="sm">
        <Group h="100%" justify="space-between" wrap="nowrap">
          <Group gap="xs" wrap="nowrap">
            <ActionIcon hiddenFrom="sm" onClick={toggleMobile} aria-label="Toggle navigation">
              <IconLayoutSidebarLeftExpand size={16} />
            </ActionIcon>
            <Brand size={18} stroke={1.8} color="var(--mantine-primary-color-filled)" />
            <Text fw={600} size="md">Pulse</Text>
          </Group>
          <Group gap="xs" wrap="nowrap">
            <StatusIndicator />
            <UnstyledButton onClick={() => spotlight.open()} aria-label="Search">
              <Group gap={6} px="xs" py={2} style={{ border: '1px solid var(--mantine-color-default-border)', borderRadius: 4 }}>
                <IconSearch size={14} />
                <Text size="xs" c="dimmed" visibleFrom="sm">Search</Text>
                <Kbd size="xs" visibleFrom="sm">⌘K</Kbd>
              </Group>
            </UnstyledButton>
            <Tooltip label={scheme === 'dark' ? 'Light mode' : 'Dark mode'}>
              <ActionIcon onClick={() => setColorScheme(scheme === 'dark' ? 'light' : 'dark')} aria-label="Toggle color scheme">
                {scheme === 'dark' ? <IconSun size={16} /> : <IconMoon size={16} />}
              </ActionIcon>
            </Tooltip>
            <UserMenu />
          </Group>
        </Group>
      </AppShell.Header>

      <AppShell.Navbar p={6}>
        <AppShell.Section grow component={ScrollArea}>
          <Stack gap={8}>
            {groups.map((g) => (
              <Stack key={g} gap={1}>
                {!collapsed && items.some((n) => n.group === g) && <Text size="xs" c="dimmed" fw={600} tt="uppercase" px={8} pt={4}>{g}</Text>}
                {items.filter((n) => n.group === g).map((n) => {
                  const active = n.to === '/' ? path === '/' : path.startsWith(n.to)
                  const link = (
                    <NavLink
                      key={n.to}
                      component={Link}
                      to={n.to}
                      active={active}
                      label={collapsed ? undefined : n.label}
                      leftSection={<n.icon size={16} stroke={1.6} />}
                      aria-label={n.label}
                    />
                  )
                  return collapsed ? <Tooltip key={n.to} label={n.label} position="right">{link}</Tooltip> : link
                })}
              </Stack>
            ))}
          </Stack>
        </AppShell.Section>
        <AppShell.Section visibleFrom="sm">
          <NavLink
            onClick={() => setCollapsed(!collapsed)}
            label={collapsed ? undefined : 'Collapse'}
            aria-label={collapsed ? 'Expand sidebar' : 'Collapse sidebar'}
            leftSection={collapsed ? <IconLayoutSidebarLeftExpand size={16} /> : <IconLayoutSidebarLeftCollapse size={16} />}
          />
        </AppShell.Section>
      </AppShell.Navbar>

      <AppShell.Main>{allowed ? children : <NoAccess />}</AppShell.Main>
      <CommandPalette />
    </AppShell>
  )
}
