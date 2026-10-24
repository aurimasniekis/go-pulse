import { Badge, Card, Code, Group, SegmentedControl, SimpleGrid, Stack, Table, Text, Title, useMantineColorScheme } from '@mantine/core'
import { createFileRoute } from '@tanstack/react-router'
import { useState } from 'react'
import { useConfig } from '@/api/queries'
import { ErrorAlert } from '@/components/ErrorAlert'
import { PageHeader } from '@/components/PageHeader'
import { formatTime, getTimeZone, setTimeZone, type TimeZonePref } from '@/lib/format'

export const Route = createFileRoute('/settings')({ component: Settings })

function Settings() {
  const cfg = useConfig()
  const { colorScheme, setColorScheme } = useMantineColorScheme()
  const [tz, setTz] = useState<TimeZonePref>(getTimeZone())
  return (
    <>
      <PageHeader
        title="Settings"
        description="Display preferences for this browser, and the server's effective configuration. pulsed is configured with PULSE_* environment variables (docs/operator/configuration.md); restart to change them."
      />
      <Card mb="sm">
        <Title order={5} mb="xs">
          Display (this browser)
        </Title>
        <SimpleGrid cols={{ base: 1, md: 2 }}>
          <Stack gap={4}>
            <Text size="sm" fw={500}>
              Time zone
            </Text>
            <SegmentedControl
              data-testid="tz"
              value={tz}
              onChange={(v) => {
                setTimeZone(v as TimeZonePref)
                setTz(v as TimeZonePref)
              }}
              data={[
                { value: 'local', label: 'Local' },
                { value: 'utc', label: 'UTC' },
              ]}
            />
            <Text size="xs" c="dimmed" data-testid="tz-example">
              Now: {formatTime(new Date().toISOString())}. Pages opened after the change use it.
            </Text>
          </Stack>
          <Stack gap={4}>
            <Text size="sm" fw={500}>
              Color scheme
            </Text>
            <SegmentedControl
              value={colorScheme}
              onChange={(v) => setColorScheme(v as 'light' | 'dark' | 'auto')}
              data={[
                { value: 'auto', label: 'System' },
                { value: 'light', label: 'Light' },
                { value: 'dark', label: 'Dark' },
              ]}
            />
          </Stack>
        </SimpleGrid>
      </Card>
      <ErrorAlert error={cfg.error} />
      <SimpleGrid cols={{ base: 1, lg: 2 }} data-testid="config">
        {(cfg.data ?? []).map((s) => (
          <Card key={s.title}>
            <Title order={5} mb="xs">
              {s.title}
            </Title>
            <Table verticalSpacing={3}>
              <Table.Tbody>
                {s.items.map((i) => (
                  <Table.Tr key={i.env}>
                    <Table.Td>
                      <Code>{i.env}</Code>
                    </Table.Td>
                    <Table.Td ta="right">
                      {i.secret ? (
                        <Badge size="sm" variant="light" color={i.value === 'set' ? 'teal' : 'gray'}>
                          {i.value}
                        </Badge>
                      ) : (
                        <Group justify="flex-end">
                          <Text size="sm" ff="monospace" style={{ wordBreak: 'break-all' }}>
                            {i.value}
                          </Text>
                        </Group>
                      )}
                    </Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </Card>
        ))}
      </SimpleGrid>
    </>
  )
}
