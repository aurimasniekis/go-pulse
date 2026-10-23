import { Group, Progress, Stack, Text, UnstyledButton } from '@mantine/core'
import type { Schemas } from '@/api/client'
import { formatBytes, formatNumber } from '@/lib/format'

/** Ranked keys with an inline magnitude bar; doubles as the table view for the charts. */
export function RankList({ items, empty = 'No data yet', onPick, testId }: { items: Schemas['KeyCount'][]; empty?: string; onPick?: (key: string) => void; testId?: string }) {
  const max = Math.max(1, ...items.map((i) => i.msgs))
  if (items.length === 0) {
    return (
      <Text size="sm" c="dimmed">
        {empty}
      </Text>
    )
  }
  return (
    <Stack gap={6} data-testid={testId}>
      {items.map((i) => (
        <UnstyledButton key={i.key} onClick={onPick ? () => onPick(i.key) : undefined} style={{ cursor: onPick ? 'pointer' : 'default' }}>
          <Group justify="space-between" gap="xs" wrap="nowrap">
            <Text size="sm" ff="monospace" truncate>
              {i.key}
            </Text>
            <Group gap={8} wrap="nowrap">
              <Text size="xs" c="dimmed">
                {formatBytes(i.bytes)}
              </Text>
              <Text size="sm" fw={600} w={70} ta="right">
                {formatNumber(i.msgs)}
              </Text>
            </Group>
          </Group>
          <Progress value={(i.msgs / max) * 100} size={4} radius="xl" mt={2} aria-hidden />
        </UnstyledButton>
      ))}
    </Stack>
  )
}
