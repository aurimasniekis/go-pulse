import { Alert, Card, Group, Progress, SimpleGrid, Stack, Text, Title, Tooltip } from '@mantine/core'
import { IconAlertTriangle } from '@tabler/icons-react'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import type { ReactNode } from 'react'
import { useOverview } from '@/api/queries'
import { ErrorAlert } from '@/components/ErrorAlert'
import { PageHeader } from '@/components/PageHeader'
import { RankList } from '@/components/RankList'
import { ThroughputChart } from '@/components/ThroughputChart'
import { formatBytes, formatNumber, timeAgo } from '@/lib/format'

export const Route = createFileRoute('/')({ component: Overview })

function Kpi({ label, value, sub, children, testId }: { label: string; value: ReactNode; sub?: ReactNode; children?: ReactNode; testId?: string }) {
  return (
    <Card data-testid={testId}>
      <Text size="xs" c="dimmed" tt="uppercase" fw={600}>
        {label}
      </Text>
      <Text fz={22} fw={650} lh={1.3}>
        {value}
      </Text>
      {children}
      {sub && (
        <Text size="xs" c="dimmed" mt={2}>
          {sub}
        </Text>
      )}
    </Card>
  )
}

/** Horizon text: needs at least a day of rollups to mean anything; very long horizons are capped. */
function horizonText(days: number | undefined, sampleDays: number | undefined): string {
  if (!days || !sampleDays || sampleDays < 1) return '—'
  if (days > 3650) return '> 10 years'
  return `${formatNumber(Math.round(days))} days`
}

function Overview() {
  const { data: o, error } = useOverview()
  const navigate = useNavigate()
  const f = o?.forecast
  const settled = !!f && f.sample_days >= 1
  const used = f ? Math.min(100, f.used_ratio * 100) : 0
  return (
    <>
      <PageHeader title="Overview" description="EVENTS at a glance. Counts come from rollups updated every 10 seconds." />
      <ErrorAlert error={error} />
      {f?.discarding_old && (
        <Alert color="orange" icon={<IconAlertTriangle size={16} />} mb="sm" title="The stream is full">
          EVENTS reached its size limit and is dropping its oldest events to make room. Raise PULSE_EVENTS_MAX_BYTES or
          free disk to keep more history.
        </Alert>
      )}
      <Stack gap="md">
        <SimpleGrid cols={{ base: 2, md: 3, xl: 6 }} spacing="md">
          <Kpi label="Stored events" value={formatNumber(o?.msgs)} sub={o?.msgs ? `oldest ${timeAgo(o.first_time)}` : 'empty'} testId="kpi-events" />
          <Kpi label="Stream size" value={formatBytes(o?.bytes)} sub={f ? `of ${formatBytes(f.max_bytes)} limit` : undefined}>
            <Tooltip label={`${used.toFixed(1)}% used`}>
              <Progress value={used} size={4} mt={6} color={used > 85 ? 'orange' : 'indigo'} />
            </Tooltip>
          </Kpi>
          <Kpi label="Last 24 hours" value={formatNumber(o?.last_24h.msgs)} sub={o ? formatBytes(o.last_24h.bytes) : undefined} testId="kpi-24h" />
          <Kpi label="Rate (last hour)" value={o ? `${formatNumber(Math.round(o.last_1h.msgs / 60))}/min` : '—'} sub={o ? `${formatNumber(o.last_1h.msgs)} events` : undefined} />
          <Kpi
            label="Subjects"
            value={formatNumber(o?.num_subjects)}
            sub={o ? `${formatNumber(o.index.indexed)} indexed${o.index.dropped ? ` · ${formatNumber(o.index.dropped)} over cap` : ''}` : undefined}
          />
          <Kpi
            label="Retention horizon"
            value={horizonText(f?.horizon_days, f?.sample_days)}
            sub={
              f?.discarding_old
                ? 'discarding oldest events'
                : !settled
                  ? 'needs a day of traffic'
                  : f?.full_at
                    ? `full ${new Date(f.full_at).toLocaleDateString()} at current rate`
                    : 'no traffic'
            }
            testId="kpi-horizon"
          />
        </SimpleGrid>
        <Card>
          <Title order={5} mb="xs">
            Throughput
          </Title>
          <ThroughputChart />
        </Card>
        <SimpleGrid cols={{ base: 1, lg: 3 }} spacing="md">
          <Card>
            <Title order={5} mb="xs">
              Top domains · 24h
            </Title>
            <RankList
              items={o?.top_prefixes_24h ?? []}
              onPick={(k) => void navigate({ to: '/explorer', search: { subject: [`${k}.>`] } })}
              testId="top-domains"
            />
          </Card>
          <Card>
            <Title order={5} mb="xs">
              Top types · 24h
            </Title>
            <RankList items={o?.top_types_24h ?? []} onPick={(k) => void navigate({ to: '/explorer', search: { type: k } })} />
          </Card>
          <Card>
            <Title order={5} mb="xs">
              Top subjects · 7d
            </Title>
            <RankList
              items={o?.top_subjects_7d ?? []}
              onPick={(k) => void navigate({ to: '/explorer', search: { subject: [k, `${k}.>`] } })}
            />
          </Card>
        </SimpleGrid>
        <Card>
          <Title order={5} mb={4}>
            Disk forecast
          </Title>
          <Group gap="xl">
            <Text size="sm">
              Average <b>{formatBytes(f?.avg_daily_bytes)}</b>/day over {f ? f.sample_days.toFixed(1) : '—'} days of rollups
            </Text>
            <Text size="sm">
              Limit <b>{formatBytes(f?.max_bytes)}</b> holds about <b>{horizonText(f?.horizon_days, f?.sample_days)}</b> of history
            </Text>
            <Text size="sm">
              {settled && f?.full_at ? (
                <>
                  Reaches the limit around <b>{new Date(f.full_at).toLocaleDateString()}</b>; after that the oldest events are dropped
                </>
              ) : (
                ' '
              )}
            </Text>
          </Group>
        </Card>
      </Stack>
    </>
  )
}
