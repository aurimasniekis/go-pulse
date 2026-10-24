import { Badge, Group } from '@mantine/core'
import { roleInfo, type Role } from '@/api/auth'

export function RoleBadges({ roles }: { roles: string[] }) {
  if (roles.length === 0) return <Badge size="sm" color="gray" variant="light">no role</Badge>
  return (
    <Group gap={4}>
      {roles.map((r) => (
        <Badge key={r} size="sm" variant="light" color={roleInfo[r as Role]?.color ?? 'gray'}>
          {r}
        </Badge>
      ))}
    </Group>
  )
}
