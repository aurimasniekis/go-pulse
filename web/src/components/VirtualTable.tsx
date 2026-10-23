import { Box, Center, Text } from '@mantine/core'
import { useVirtualizer } from '@tanstack/react-virtual'
import { useRef, type CSSProperties, type ReactNode } from 'react'
import classes from './VirtualTable.module.css'

export interface Column<T> {
  key: string
  header: ReactNode
  /** CSS grid track, e.g. "90px", "minmax(200px, 1fr)". */
  width: string
  render: (row: T) => ReactNode
  align?: 'left' | 'right'
}

interface Props<T> {
  rows: T[]
  columns: Column<T>[]
  rowKey: (row: T) => string | number
  onRowClick?: (row: T) => void
  selectedKey?: string | number
  height?: CSSProperties['height']
  empty?: ReactNode
  footer?: ReactNode
  rowHeight?: number
  testId?: string
}

/** Dense, virtualized grid table for large lists (PL-UI-007). */
export function VirtualTable<T>({ rows, columns, rowKey, onRowClick, selectedKey, height = 520, empty, footer, rowHeight = 28, testId }: Props<T>) {
  const parent = useRef<HTMLDivElement>(null)
  const virt = useVirtualizer({ count: rows.length, getScrollElement: () => parent.current, estimateSize: () => rowHeight, overscan: 12 })
  const template = columns.map((c) => c.width).join(' ')
  return (
    <Box className={classes.wrap} data-testid={testId}>
      <div className={classes.header} style={{ gridTemplateColumns: template }}>
        {columns.map((c) => (
          <div key={c.key} className={classes.cell} style={{ textAlign: c.align }}>
            {c.header}
          </div>
        ))}
      </div>
      <div ref={parent} className={classes.body} style={{ height }}>
        {rows.length === 0 ? (
          <Center h="100%">{empty ?? <Text c="dimmed" size="sm">No rows</Text>}</Center>
        ) : (
          <div style={{ height: virt.getTotalSize(), position: 'relative' }}>
            {virt.getVirtualItems().map((vi) => {
              const row = rows[vi.index]
              const key = rowKey(row)
              return (
                <div
                  key={key}
                  role="row"
                  data-selected={key === selectedKey || undefined}
                  className={classes.row}
                  onClick={onRowClick ? () => onRowClick(row) : undefined}
                  style={{ gridTemplateColumns: template, height: vi.size, transform: `translateY(${vi.start}px)`, cursor: onRowClick ? 'pointer' : undefined }}
                >
                  {columns.map((c) => (
                    <div key={c.key} className={classes.cell} style={{ textAlign: c.align }}>
                      {c.render(row)}
                    </div>
                  ))}
                </div>
              )
            })}
          </div>
        )}
      </div>
      {footer && <div className={classes.footer}>{footer}</div>}
    </Box>
  )
}
