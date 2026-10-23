import { Badge, Code, Group, ScrollArea, SegmentedControl, Text } from '@mantine/core'
import { useMemo, useState } from 'react'
import { hexDump, payloadBytes, prettyJSON } from '@/lib/format'

type Mode = 'json' | 'text' | 'hex'

/** Shows a payload as pretty JSON, text or a hex dump (PL-EXP-008). */
export function PayloadView({ data, encoding, contentType }: { data: string; encoding: 'utf8' | 'base64'; contentType?: string }) {
  const json = useMemo(() => (encoding === 'utf8' ? prettyJSON(data) : undefined), [data, encoding])
  const bytes = useMemo(() => payloadBytes(data, encoding), [data, encoding])
  const initial: Mode = json ? 'json' : encoding === 'utf8' ? 'text' : 'hex'
  const [mode, setMode] = useState<Mode>(initial)
  const options = [
    ...(json ? [{ label: 'JSON', value: 'json' }] : []),
    ...(encoding === 'utf8' ? [{ label: 'Text', value: 'text' }] : []),
    { label: 'Hex', value: 'hex' },
  ]
  const body = mode === 'json' ? json : mode === 'text' ? data : hexDump(bytes)
  return (
    <div>
      <Group justify="space-between" mb={6}>
        <SegmentedControl size="xs" value={mode} onChange={(v) => setMode(v as Mode)} data={options} />
        <Group gap={6}>
          {contentType && (
            <Badge variant="outline" color="gray">
              {contentType}
            </Badge>
          )}
          <Text size="xs" c="dimmed">
            {bytes.length} bytes
          </Text>
        </Group>
      </Group>
      {bytes.length === 0 ? (
        <Text size="sm" c="dimmed">
          Empty payload
        </Text>
      ) : (
        <ScrollArea.Autosize mah={420} type="auto">
          <Code block data-testid="payload" style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>
            {body}
          </Code>
        </ScrollArea.Autosize>
      )}
    </div>
  )
}
