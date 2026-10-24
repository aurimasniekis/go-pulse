import { Badge, Group, Tooltip } from '@mantine/core'
import { useCan } from '@/api/auth'
import { useSystemInfo } from '@/api/queries'

/** Header badge: pulsed API reachable + NATS connection (PL-UI-003). */
export function StatusIndicator() {
  const can = useCan('dashboard.read')
  const { data, isError } = useSystemInfo(can)
  let color = 'gray'
  let label = 'connecting'
  if (!can) return null
  if (isError) {
    color = 'red'
    label = 'API unreachable'
  } else if (data) {
    color = data.nats.connected ? (data.ready ? 'teal' : 'yellow') : 'red'
    label = data.nats.connected ? `NATS ${data.nats.version}` : 'NATS down'
  }
  return (
    <Tooltip
      label={data ? `pulsed ${data.version} · ${data.profile} · rtt ${data.nats.rtt_ms.toFixed(1)} ms` : label}
    >
      <Group gap={4} wrap="nowrap" data-testid="status-indicator">
        <Badge color={color} variant="dot" size="sm">
          {label}
        </Badge>
      </Group>
    </Tooltip>
  )
}
