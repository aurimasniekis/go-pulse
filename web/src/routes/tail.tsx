import { Alert, Badge, Button, Group, Paper, Select, TagsInput, Text } from '@mantine/core'
import { IconPlayerPause, IconPlayerPlay, IconPlayerStop, IconTrash } from '@tabler/icons-react'
import { createFileRoute } from '@tanstack/react-router'
import { useEffect, useRef, useState } from 'react'
import type { EventDTO } from '@/api/client'
import { useStreams } from '@/api/queries'
import { EventDrawer } from '@/components/EventDrawer'
import { eventColumns } from '@/components/eventColumns'
import { PageHeader } from '@/components/PageHeader'
import { VirtualTable } from '@/components/VirtualTable'

export const Route = createFileRoute('/tail')({ component: Tail })

const MAX_ROWS = 2000

interface Notice {
  dropped: number
  reason: string
  rate: number
}

type Status = 'idle' | 'connecting' | 'live' | 'error'

const statusColor: Record<Status, string> = { idle: 'gray', connecting: 'yellow', live: 'teal', error: 'red' }

function Tail() {
  const streams = useStreams()
  const [stream, setStream] = useState('EVENTS')
  const [subjects, setSubjects] = useState<string[]>([])
  const [status, setStatus] = useState<Status>('idle')
  const [rows, setRows] = useState<EventDTO[]>([])
  const [paused, setPaused] = useState(false)
  const [dropped, setDropped] = useState(0)
  const [lastNotice, setLastNotice] = useState<Notice | null>(null)
  const [rate, setRate] = useState(0)
  const [selected, setSelected] = useState<EventDTO | null>(null)
  const es = useRef<EventSource | null>(null)
  const buffer = useRef<EventDTO[]>([])
  const pausedRef = useRef(paused)
  const counter = useRef(0)

  useEffect(() => {
    pausedRef.current = paused
  }, [paused])

  // Flush buffered events at most 4×/s so bursts do not re-render per message.
  useEffect(() => {
    const flush = setInterval(() => {
      if (buffer.current.length && !pausedRef.current) {
        const batch = buffer.current.reverse()
        buffer.current = []
        setRows((r) => [...batch, ...r].slice(0, MAX_ROWS))
      }
    }, 250)
    const meter = setInterval(() => {
      setRate(counter.current)
      counter.current = 0
    }, 1000)
    return () => {
      clearInterval(flush)
      clearInterval(meter)
      es.current?.close()
    }
  }, [])

  const stop = () => {
    es.current?.close()
    es.current = null
    setStatus('idle')
  }

  const start = () => {
    es.current?.close()
    const qs = new URLSearchParams({ stream })
    subjects.forEach((s) => qs.append('subject', s))
    const src = new EventSource(`/api/admin/v1/tail?${qs}`)
    es.current = src
    setStatus('connecting')
    src.onopen = () => setStatus('live')
    src.onerror = () => setStatus(src.readyState === EventSource.CLOSED ? 'error' : 'connecting')
    src.addEventListener('event', (m) => {
      counter.current++
      if (pausedRef.current) return
      buffer.current.push(JSON.parse((m as MessageEvent<string>).data) as EventDTO)
      if (buffer.current.length > MAX_ROWS) buffer.current.splice(0, buffer.current.length - MAX_ROWS)
    })
    src.addEventListener('notice', (m) => {
      const n = JSON.parse((m as MessageEvent<string>).data) as Notice
      setLastNotice(n)
      setDropped((d) => d + (n.dropped || 0))
      if (n.reason.startsWith('error')) {
        src.close()
        setStatus('error')
      }
    })
  }

  const running = status === 'live' || status === 'connecting'
  return (
    <>
      <PageHeader
        title="Live tail"
        description="New events as they arrive. Limited to 200 events/s per session; excess events are sampled out and counted."
      />
      <Paper withBorder p="sm" mb="sm">
        <Group align="flex-end" gap="xs">
          <Select
            label="Stream"
            w={180}
            data={(streams.data ?? []).map((s) => s.name)}
            value={stream}
            onChange={(v) => setStream(v ?? 'EVENTS')}
            disabled={running}
            allowDeselect={false}
          />
          <TagsInput label="Subjects" placeholder="all subjects" value={subjects} onChange={setSubjects} disabled={running} style={{ flex: 1 }} />
          {running ? (
            <Button color="red" variant="light" leftSection={<IconPlayerStop size={14} />} onClick={stop}>
              Stop
            </Button>
          ) : (
            <Button leftSection={<IconPlayerPlay size={14} />} onClick={start}>
              Start
            </Button>
          )}
          <Button
            variant="default"
            leftSection={paused ? <IconPlayerPlay size={14} /> : <IconPlayerPause size={14} />}
            onClick={() => setPaused((p) => !p)}
            disabled={!running}
          >
            {paused ? 'Resume' : 'Pause'}
          </Button>
          <Button variant="subtle" color="gray" leftSection={<IconTrash size={14} />} onClick={() => setRows([])}>
            Clear
          </Button>
        </Group>
      </Paper>
      {lastNotice?.reason.startsWith('error') && (
        <Alert color="red" mb="sm" title="Tail failed">
          {lastNotice.reason}
        </Alert>
      )}
      {dropped > 0 && (
        <Alert color="yellow" mb="sm" title="Events sampled out" withCloseButton onClose={() => setDropped(0)}>
          {dropped} events were not shown ({lastNotice?.reason === 'slow_client' ? 'the browser fell behind' : `rate limit ${lastNotice?.rate}/s`}).
          They are stored; the Explorer shows all of them.
        </Alert>
      )}
      <VirtualTable
        testId="tail-table"
        rows={rows}
        columns={eventColumns}
        rowKey={(e) => `${e.stream}:${e.seq}`}
        onRowClick={setSelected}
        height="calc(100vh - 300px)"
        empty={
          <Text c="dimmed" size="sm">
            {status === 'idle' ? 'Press Start to tail new events' : 'Waiting for events…'}
          </Text>
        }
        footer={
          <Group gap="xs">
            <Badge color={statusColor[status]} variant="dot" data-testid="tail-status">
              {status}
            </Badge>
            <Text size="xs" c="dimmed">
              {rows.length} shown (max {MAX_ROWS}) · {rate}/s{paused ? ' · paused' : ''}
            </Text>
          </Group>
        }
      />
      <EventDrawer event={selected} onClose={() => setSelected(null)} />
    </>
  )
}
