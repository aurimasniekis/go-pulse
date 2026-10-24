import { Spotlight, type SpotlightActionData } from '@mantine/spotlight'
import { IconSearch, IconUser } from '@tabler/icons-react'
import { useNavigate } from '@tanstack/react-router'
import { useMemo } from 'react'
import { useMe } from '@/api/auth'
import { visibleNav } from '@/lib/nav'

export function CommandPalette() {
  const navigate = useNavigate()
  const me = useMe().data
  const actions = useMemo<SpotlightActionData[]>(
    () =>
      [...visibleNav(me?.permissions ?? []), { to: '/account', label: 'Account', icon: IconUser }].map((n) => ({
        id: n.to,
        label: n.label,
        description: `Go to ${n.label}`,
        leftSection: <n.icon size={16} stroke={1.6} />,
        onClick: () => void navigate({ to: n.to }),
      })),
    [navigate, me],
  )
  return (
    <Spotlight
      actions={actions}
      shortcut={['mod + K', '/']}
      nothingFound="Nothing found"
      highlightQuery
      searchProps={{ leftSection: <IconSearch size={16} />, placeholder: 'Jump to…' }}
    />
  )
}
