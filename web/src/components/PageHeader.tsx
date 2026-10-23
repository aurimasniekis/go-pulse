import { Group, Stack, Text, Title } from '@mantine/core'
import type { ReactNode } from 'react'

export function PageHeader({ title, description, actions }: { title: string; description?: string; actions?: ReactNode }) {
  return (
    <Group justify="space-between" align="flex-end" mb="md" wrap="nowrap">
      <Stack gap={2}>
        <Title order={3}>{title}</Title>
        {description && <Text size="sm" c="dimmed">{description}</Text>}
      </Stack>
      {actions && <Group gap="xs">{actions}</Group>}
    </Group>
  )
}
