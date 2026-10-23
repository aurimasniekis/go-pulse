import {
  IconActivity, IconAlertTriangle, IconArchive, IconBinaryTree2, IconDatabase, IconFileText, IconGauge, IconKey,
  IconLayoutDashboard, IconListSearch, IconPlugConnected, IconServer, IconSettings, IconShieldLock, IconTicket, IconWebhook,
} from '@tabler/icons-react'
import type { ComponentType } from 'react'

export interface NavItem {
  to: string
  label: string
  icon: ComponentType<{ size?: number; stroke?: number }>
  group: 'Events' | 'Access' | 'Operations'
}

export const nav: NavItem[] = [
  { to: '/', label: 'Overview', icon: IconLayoutDashboard, group: 'Events' },
  { to: '/explorer', label: 'Explorer', icon: IconListSearch, group: 'Events' },
  { to: '/tail', label: 'Live tail', icon: IconActivity, group: 'Events' },
  { to: '/subjects', label: 'Subjects', icon: IconBinaryTree2, group: 'Events' },
  { to: '/streams', label: 'Streams', icon: IconDatabase, group: 'Events' },
  { to: '/kv', label: 'KV & Objects', icon: IconArchive, group: 'Events' },
  { to: '/clients', label: 'Clients', icon: IconPlugConnected, group: 'Access' },
  { to: '/tokens', label: 'HTTP tokens', icon: IconTicket, group: 'Access' },
  { to: '/webhooks', label: 'Webhooks', icon: IconWebhook, group: 'Access' },
  { to: '/security', label: 'Security', icon: IconShieldLock, group: 'Access' },
  { to: '/audit', label: 'Audit', icon: IconFileText, group: 'Access' },
  { to: '/system', label: 'System', icon: IconServer, group: 'Operations' },
  { to: '/alerts', label: 'Alerts', icon: IconAlertTriangle, group: 'Operations' },
  { to: '/backups', label: 'Backups', icon: IconKey, group: 'Operations' },
  { to: '/settings', label: 'Settings', icon: IconSettings, group: 'Operations' },
]

export const brandIcon = IconGauge
