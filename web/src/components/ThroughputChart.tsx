import { Group, SegmentedControl, Stack, Text, useComputedColorScheme, useMantineTheme } from '@mantine/core'
import { useMemo, useState } from 'react'
import type { Schemas } from '@/api/client'
import { useThroughput } from '@/api/queries'
import { formatBytes, formatCompact, formatNumber } from '@/lib/format'
import { chartTokens } from '@/theme/chart'
import { EChart, type EChartsOption } from './EChart'
import { ErrorAlert } from './ErrorAlert'

export type RangeKey = '1h' | '24h' | '7d' | '30d' | '1y'
type Dim = 'total' | 'type' | 'p2'
type Measure = 'msgs' | 'bytes'

const stepLabel: Record<number, string> = { 60: 'per minute', 3600: 'per hour', 86400: 'per day' }

function buildOption(set: Schemas['SeriesSet'], measure: Measure, tokens: ReturnType<typeof chartTokens>): EChartsOption {
  const multi = set.series.length > 1
  const fmt = measure === 'bytes' ? (v: number) => formatBytes(v, 0) : (v: number) => formatCompact(v)
  return {
    animation: false,
    textStyle: { fontFamily: tokens.fontFamily, color: tokens.text },
    grid: { left: 8, right: 16, top: multi ? 36 : 12, bottom: 8, containLabel: true },
    legend: multi
      ? { top: 0, left: 0, icon: 'roundRect', itemWidth: 10, itemHeight: 4, textStyle: { color: tokens.muted, fontSize: 11 } }
      : undefined,
    tooltip: {
      trigger: 'axis',
      axisPointer: { type: 'line', lineStyle: { color: tokens.axis, width: 1 } },
      backgroundColor: tokens.tooltipBg,
      borderColor: tokens.tooltipBorder,
      textStyle: { color: tokens.text, fontSize: 12 },
      valueFormatter: (v: unknown) => (measure === 'bytes' ? formatBytes(Number(v)) : formatNumber(Number(v))),
    },
    xAxis: {
      type: 'time',
      axisLine: { lineStyle: { color: tokens.axis } },
      axisTick: { show: false },
      axisLabel: { color: tokens.muted, fontSize: 11, hideOverlap: true },
      splitLine: { show: false },
    },
    yAxis: {
      type: 'value',
      min: 0,
      axisLabel: { color: tokens.muted, fontSize: 11, formatter: fmt },
      splitLine: { lineStyle: { color: tokens.grid, width: 1, type: 'solid' } },
    },
    series: set.series.map((s, i) => ({
      name: s.key === '' ? 'all events' : s.key,
      type: 'line',
      showSymbol: false,
      symbolSize: 8,
      lineStyle: { width: 2, cap: 'round', join: 'round' },
      itemStyle: { color: tokens.series[i % tokens.series.length], borderColor: tokens.surface, borderWidth: 2 },
      areaStyle: multi ? undefined : { opacity: 0.1 },
      emphasis: { focus: multi ? 'series' : 'none' },
      data: s.points.map((p) => [p.t * 1000, measure === 'bytes' ? p.bytes : p.msgs]),
    })),
  }
}

/** Throughput over time by total, type or domain (PL-ROLL-006). One measure at a time: never a dual axis. */
export function ThroughputChart({ stream = 'EVENTS' }: { stream?: string }) {
  const [range, setRange] = useState<RangeKey>('24h')
  const [dim, setDim] = useState<Dim>('total')
  const [measure, setMeasure] = useState<Measure>('msgs')
  const q = useThroughput(stream, range, dim)
  const theme = useMantineTheme()
  const scheme = useComputedColorScheme('light')
  const tokens = useMemo(() => chartTokens(theme, scheme), [theme, scheme])
  const option = useMemo(() => (q.data ? buildOption(q.data, measure, tokens) : undefined), [q.data, measure, tokens])
  const total = q.data?.series.reduce((n, s) => n + s.points.reduce((m, p) => m + (measure === 'bytes' ? p.bytes : p.msgs), 0), 0)
  return (
    <Stack gap="xs">
      <Group justify="space-between" wrap="wrap" gap="xs">
        <Group gap="xs">
          <SegmentedControl
            size="xs"
            value={dim}
            onChange={(v) => setDim(v as Dim)}
            data={[
              { label: 'Total', value: 'total' },
              { label: 'By type', value: 'type' },
              { label: 'By domain', value: 'p2' },
            ]}
          />
          <SegmentedControl
            size="xs"
            value={measure}
            onChange={(v) => setMeasure(v as Measure)}
            data={[
              { label: 'Events', value: 'msgs' },
              { label: 'Bytes', value: 'bytes' },
            ]}
          />
        </Group>
        <SegmentedControl
          size="xs"
          value={range}
          onChange={(v) => setRange(v as RangeKey)}
          data={['1h', '24h', '7d', '30d', '1y']}
          data-testid="range-select"
        />
      </Group>
      <ErrorAlert error={q.error} />
      <Text size="xs" c="dimmed">
        {q.data ? `${measure === 'bytes' ? formatBytes(total ?? 0) : formatNumber(total ?? 0)} in ${range} · ${stepLabel[q.data.step] ?? ''}` : ' '}
        {dim !== 'total' && q.data ? ` · top ${q.data.series.length} ${dim === 'type' ? 'types' : 'domains'}` : ''}
      </Text>
      {option ? <EChart option={option} height={260} testId="throughput-chart" /> : <div style={{ height: 260 }} />}
    </Stack>
  )
}
