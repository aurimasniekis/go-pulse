import type { MantineTheme } from '@mantine/core'

/**
 * Categorical series colors (fixed order, never cycled), validated with the dataviz validator against the Mantine
 * surfaces: light #ffffff, dark #242424 (both pass every hard gate; light has a contrast WARN for slots 3–5, so every
 * multi-series chart ships a legend and a table view).
 */
export const seriesLight = ['#2a78d6', '#eb6834', '#1baf7a', '#eda100', '#e87ba4', '#008300']
export const seriesDark = ['#3987e5', '#d95926', '#199e70', '#c98500', '#d55181', '#008300']

export interface ChartTokens {
  series: string[]
  text: string
  muted: string
  grid: string
  axis: string
  surface: string
  tooltipBg: string
  tooltipBorder: string
  fontFamily: string
}

/** Chart tokens derived from the Mantine theme for one color scheme (PL-UI-006). */
export function chartTokens(theme: MantineTheme, scheme: 'light' | 'dark'): ChartTokens {
  const dark = scheme === 'dark'
  return {
    series: dark ? seriesDark : seriesLight,
    text: dark ? theme.colors.dark[0] : theme.colors.gray[8],
    muted: dark ? theme.colors.dark[2] : theme.colors.gray[6],
    grid: dark ? theme.colors.dark[4] : theme.colors.gray[2],
    axis: dark ? theme.colors.dark[3] : theme.colors.gray[4],
    surface: dark ? theme.colors.dark[7] : theme.white,
    tooltipBg: dark ? theme.colors.dark[6] : theme.white,
    tooltipBorder: dark ? theme.colors.dark[4] : theme.colors.gray[3],
    fontFamily: theme.fontFamily ?? 'sans-serif',
  }
}
