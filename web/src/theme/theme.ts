import { createTheme, type MantineColorsTuple } from '@mantine/core'

// Dense observability look (ADR-0014): 4px radius, compact controls, Inter + JetBrains Mono, grays + indigo.
const indigo: MantineColorsTuple = [
  '#eef0ff', '#dcdefa', '#b5b9ef', '#8c92e5', '#6970dc', '#535bd7', '#4750d6', '#3942be', '#3139ab', '#263098',
]

export const theme = createTheme({
  primaryColor: 'indigo',
  primaryShade: { light: 6, dark: 5 },
  colors: { indigo },
  fontFamily: "'Inter Variable', Inter, system-ui, -apple-system, 'Segoe UI', sans-serif",
  fontFamilyMonospace: "'JetBrains Mono Variable', 'JetBrains Mono', ui-monospace, SFMono-Regular, Menlo, monospace",
  headings: { fontFamily: "'Inter Variable', Inter, system-ui, sans-serif", fontWeight: '600' },
  defaultRadius: 'sm',
  radius: { xs: '2px', sm: '4px', md: '4px', lg: '6px', xl: '8px' },
  fontSizes: { xs: '11px', sm: '12px', md: '13px', lg: '15px', xl: '18px' },
  spacing: { xs: '6px', sm: '8px', md: '12px', lg: '16px', xl: '24px' },
  cursorType: 'pointer',
  components: {
    Button: { defaultProps: { size: 'xs' } },
    ActionIcon: { defaultProps: { size: 'sm', variant: 'subtle', color: 'gray' } },
    TextInput: { defaultProps: { size: 'xs' } },
    PasswordInput: { defaultProps: { size: 'xs' } },
    NumberInput: { defaultProps: { size: 'xs' } },
    Select: { defaultProps: { size: 'xs' } },
    MultiSelect: { defaultProps: { size: 'xs' } },
    Textarea: { defaultProps: { size: 'xs' } },
    SegmentedControl: { defaultProps: { size: 'xs' } },
    Badge: { defaultProps: { size: 'sm', radius: 'sm', variant: 'light' } },
    Table: { defaultProps: { fz: 'sm', verticalSpacing: 4, horizontalSpacing: 'sm', highlightOnHover: true } },
    Card: { defaultProps: { withBorder: true, padding: 'md', radius: 'sm' } },
    Paper: { defaultProps: { radius: 'sm' } },
    Tooltip: { defaultProps: { openDelay: 300, fz: 'xs' } },
    NavLink: { defaultProps: { py: 4 } },
  },
})
