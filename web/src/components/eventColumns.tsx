import { Badge, Text } from '@mantine/core'
import type { EventDTO } from '@/api/client'
import { formatBytes, formatTime, preview } from '@/lib/format'
import type { Column } from './VirtualTable'

/** Columns shared by the Explorer and Live tail tables. */
export const eventColumns: Column<EventDTO>[] = [
  {
    key: 'seq',
    header: 'Seq',
    width: '80px',
    align: 'right',
    render: (e) => (
      <Text size="sm" ff="monospace">
        {e.seq}
      </Text>
    ),
  },
  {
    key: 'time',
    header: 'Stored',
    width: '190px',
    render: (e) => (
      <Text size="sm" ff="monospace">
        {formatTime(e.stream_time)}
      </Text>
    ),
  },
  {
    key: 'subject',
    header: 'Subject',
    width: 'minmax(180px, 1.2fr)',
    render: (e) => (
      <Text size="sm" ff="monospace" c={e.subject_valid ? undefined : 'orange'} truncate>
        {e.subject}
      </Text>
    ),
  },
  {
    key: 'type',
    header: 'Type',
    width: 'minmax(120px, 0.8fr)',
    render: (e) =>
      e.untyped ? (
        <Badge color="gray" variant="outline">
          untyped
        </Badge>
      ) : (
        <Text size="sm" truncate>
          {e.type}
        </Text>
      ),
  },
  { key: 'size', header: 'Size', width: '80px', align: 'right', render: (e) => <Text size="sm">{formatBytes(e.size)}</Text> },
  {
    key: 'preview',
    header: 'Payload',
    width: 'minmax(200px, 2fr)',
    render: (e) => (
      <Text size="sm" ff="monospace" c="dimmed" truncate>
        {preview(e.data, e.encoding)}
      </Text>
    ),
  },
]
