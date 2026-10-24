import {
  IconActivity, IconAlertTriangle, IconArchive, IconBinaryTree2, IconDatabase, IconFileText, IconGauge, IconKey,
  IconLayoutDashboard, IconListSearch, IconPlugConnected, IconServer, IconSettings, IconShieldLock, IconTicket, IconUsers, IconWebhook,
} from '@tabler/icons-react'
import type { ComponentType } from 'react'
import type { Perm } from '@/api/auth'

export interface NavItem {
  to: string
  label: string
  icon: ComponentType<{ size?: number; stroke?: number }>
  group: 'Events' | 'Access' | 'Operations'
  /** The permission the page needs to load (entries without it are hidden). */
  perm: Perm
}

export const nav: NavItem[] = [
  { to: '/', label: 'Overview', icon: IconLayoutDashboard, group: 'Events', perm: 'dashboard.read' },
  { to: '/explorer', label: 'Explorer', icon: IconListSearch, group: 'Events', perm: 'events.read' },
  { to: '/tail', label: 'Live tail', icon: IconActivity, group: 'Events', perm: 'events.read' },
  { to: '/subjects', label: 'Subjects', icon: IconBinaryTree2, group: 'Events', perm: 'dashboard.read' },
  { to: '/streams', label: 'Streams', icon: IconDatabase, group: 'Events', perm: 'dashboard.read' },
  { to: '/kv', label: 'KV & Objects', icon: IconArchive, group: 'Events', perm: 'dashboard.read' },
  { to: '/clients', label: 'Clients', icon: IconPlugConnected, group: 'Access', perm: 'clients.read' },
  { to: '/tokens', label: 'HTTP tokens', icon: IconTicket, group: 'Access', perm: 'clients.read' },
  { to: '/webhooks', label: 'Webhooks', icon: IconWebhook, group: 'Access', perm: 'webhooks.read' },
  { to: '/security', label: 'Security', icon: IconShieldLock, group: 'Access', perm: 'security.read' },
  { to: '/audit', label: 'Audit', icon: IconFileText, group: 'Access', perm: 'audit.read' },
  { to: '/users', label: 'Users', icon: IconUsers, group: 'Access', perm: 'users.read' },
  { to: '/system', label: 'System', icon: IconServer, group: 'Operations', perm: 'system.read' },
  { to: '/alerts', label: 'Alerts', icon: IconAlertTriangle, group: 'Operations', perm: 'alerts.read' },
  { to: '/backups', label: 'Backups', icon: IconKey, group: 'Operations', perm: 'backups.read' },
  { to: '/settings', label: 'Settings', icon: IconSettings, group: 'Operations', perm: 'system.read' },
]

export const brandIcon = IconGauge

/** The nav entries a user with these permissions can open. */
export function visibleNav(perms: readonly string[]): NavItem[] {
  return nav.filter((n) => perms.includes(n.perm))
}
