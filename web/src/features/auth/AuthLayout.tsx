import { Card, Center, Group, Stack, Text, Title } from '@mantine/core'
import type { ReactNode } from 'react'
import { brandIcon as Brand } from '@/lib/nav'

/** The frame of the sign-in pages: no sidebar, no API calls beyond the page's own. */
export function AuthLayout({ title, subtitle, children }: { title: string; subtitle?: ReactNode; children: ReactNode }) {
  return (
    <Center mih="100vh" p="md" bg="var(--mantine-color-body)">
      <Stack w="100%" maw={420} gap="md">
        <Group gap="xs" justify="center">
          <Brand size={22} stroke={1.8} color="var(--mantine-primary-color-filled)" />
          <Text fw={600} size="lg">
            Pulse
          </Text>
        </Group>
        <Card withBorder padding="lg">
          <Stack gap="sm">
            <div>
              <Title order={3}>{title}</Title>
              {subtitle && (
                <Text size="sm" c="dimmed" mt={4}>
                  {subtitle}
                </Text>
              )}
            </div>
            {children}
          </Stack>
        </Card>
      </Stack>
    </Center>
  )
}
