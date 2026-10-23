import { Box } from '@mantine/core'
import { LineChart } from 'echarts/charts'
import { GridComponent, LegendComponent, TooltipComponent } from 'echarts/components'
import * as echarts from 'echarts/core'
import { SVGRenderer } from 'echarts/renderers'
import { useEffect, useRef } from 'react'

echarts.use([LineChart, GridComponent, TooltipComponent, LegendComponent, SVGRenderer])

export type EChartsOption = echarts.EChartsCoreOption

/** Thin ECharts wrapper: SVG renderer, resizes with its box, replaces the option on change. */
export function EChart({ option, height = 280, testId }: { option: EChartsOption; height?: number; testId?: string }) {
  const el = useRef<HTMLDivElement>(null)
  const chart = useRef<echarts.ECharts | null>(null)

  useEffect(() => {
    if (!el.current) return
    const c = echarts.init(el.current, undefined, { renderer: 'svg' })
    chart.current = c
    const ro = new ResizeObserver(() => c.resize())
    ro.observe(el.current)
    return () => {
      ro.disconnect()
      c.dispose()
      chart.current = null
    }
  }, [])

  useEffect(() => {
    chart.current?.setOption(option, { notMerge: true })
  }, [option])

  return <Box ref={el} h={height} w="100%" data-testid={testId} />
}
