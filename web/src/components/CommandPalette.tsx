import { Spotlight, type SpotlightActionData } from '@mantine/spotlight'
import { IconSearch } from '@tabler/icons-react'
import { useNavigate } from '@tanstack/react-router'
import { useMemo } from 'react'
import { nav } from '@/lib/nav'

export function CommandPalette() {
  const navigate = useNavigate()
  const actions = useMemo<SpotlightActionData[]>(
    () =>
      nav.map((n) => ({
        id: n.to,
        label: n.label,
        description: `Go to ${n.label}`,
        leftSection: <n.icon size={16} stroke={1.6} />,
        onClick: () => void navigate({ to: n.to }),
      })),
    [navigate],
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
