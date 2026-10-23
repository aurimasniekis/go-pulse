import { Badge, Group, Tooltip } from '@mantine/core'
import { useSystemInfo } from '@/api/queries'

/** Header badge: pulsed API reachable + NATS connection (PL-UI-003). */
export function StatusIndicator() {
  const { data, isError } = useSystemInfo()
  let color = 'gray'
  let label = 'connecting'
  if (isError) {
    color = 'red'
    label = 'API unreachable'
  } else if (data) {
    color = data.nats.connected ? (data.ready ? 'teal' : 'yellow') : 'red'
    label = data.nats.connected ? `NATS ${data.nats.version}` : 'NATS down'
  }
  return (
    <Tooltip
      label={data ? `pulsed ${data.version} · ${data.profile} · auth ${data.auth_mode} · rtt ${data.nats.rtt_ms.toFixed(1)} ms` : label}
    >
      <Group gap={4} wrap="nowrap" data-testid="status-indicator">
        <Badge color={color} variant="dot" size="sm">
          {label}
        </Badge>
      </Group>
    </Tooltip>
  )
}
