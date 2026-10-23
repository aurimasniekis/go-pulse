import { Button, Group, Modal, NumberInput, Select, SimpleGrid, Stack, Switch, TagsInput, TextInput } from '@mantine/core'
import { useForm } from '@mantine/form'
import { useEffect } from 'react'
import type { StreamDTO } from '@/api/client'
import { useStreamMutations, type StreamInput } from '@/api/queries'
import { formatBytes, formatDuration } from '@/lib/format'
import { notifyErr, notifyOk } from '@/lib/notify'
import { parseDuration, parseSize } from '@/lib/search'

interface Values {
  name: string
  description: string
  subjects: string[]
  storage: 'file' | 'memory'
  maxBytes: string
  maxAge: string
  maxMsgs: number | ''
  discard: 'old' | 'new'
  compression: boolean
  duplicates: number
  allowDirect: boolean
}

const empty: Values = {
  name: '',
  description: '',
  subjects: [],
  storage: 'file',
  maxBytes: '1GiB',
  maxAge: '',
  maxMsgs: '',
  discard: 'old',
  compression: true,
  duplicates: 120,
  allowDirect: true,
}

function fromStream(s: StreamDTO): Values {
  return {
    name: s.name,
    description: s.description,
    subjects: s.subjects,
    storage: s.storage === 'Memory' || s.storage === 'memory' ? 'memory' : 'file',
    maxBytes: s.max_bytes < 0 ? 'unlimited' : formatBytes(s.max_bytes).replace(' ', ''),
    maxAge: s.max_age_seconds ? formatDuration(s.max_age_seconds).split(' ')[0] : '',
    maxMsgs: s.max_msgs < 0 ? '' : s.max_msgs,
    discard: s.discard.toLowerCase().includes('new') ? 'new' : 'old',
    compression: s.compression.toLowerCase() === 's2',
    duplicates: s.duplicates_seconds,
    allowDirect: s.allow_direct,
  }
}

/** Create or edit a user stream (PL-BUS-005). */
export function StreamForm({ opened, onClose, stream }: { opened: boolean; onClose: () => void; stream?: StreamDTO }) {
  const { create, update } = useStreamMutations()
  const editing = !!stream
  const form = useForm<Values>({
    initialValues: empty,
    validate: {
      name: (v) => (/^[A-Za-z0-9_-]{1,64}$/.test(v) ? null : 'Letters, digits, _ and -, up to 64'),
      subjects: (v) => (v.length ? null : 'At least one subject'),
      maxBytes: (v) => {
        try {
          parseSize(v)
          return null
        } catch (e) {
          return (e as Error).message
        }
      },
      maxAge: (v) => {
        try {
          parseDuration(v)
          return null
        } catch (e) {
          return (e as Error).message
        }
      },
    },
  })
  useEffect(() => {
    if (opened) form.setValues(stream ? fromStream(stream) : empty)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [opened, stream])

  const submit = form.onSubmit(async (v) => {
    const body: StreamInput = {
      name: v.name,
      description: v.description || undefined,
      subjects: v.subjects,
      storage: v.storage,
      max_bytes: parseSize(v.maxBytes) ?? -1,
      max_age_seconds: parseDuration(v.maxAge),
      max_msgs: v.maxMsgs === '' ? -1 : v.maxMsgs,
      discard: v.discard,
      compression: v.compression,
      duplicates_seconds: v.duplicates,
      allow_direct: v.allowDirect,
    }
    try {
      await (editing ? update : create).mutateAsync(body)
      notifyOk(editing ? `Stream ${v.name} updated` : `Stream ${v.name} created`)
      onClose()
    } catch (e) {
      notifyErr(e)
    }
  })

  return (
    <Modal opened={opened} onClose={onClose} title={editing ? `Edit ${stream?.name}` : 'Create stream'} size="lg">
      <form onSubmit={submit}>
        <Stack gap="sm">
          <SimpleGrid cols={2} spacing="sm">
            <TextInput label="Name" required disabled={editing} {...form.getInputProps('name')} />
            <Select
              label="Storage"
              data={['file', 'memory']}
              disabled={editing}
              allowDeselect={false}
              {...form.getInputProps('storage')}
            />
          </SimpleGrid>
          <TextInput label="Description" {...form.getInputProps('description')} />
          <TagsInput
            label="Subjects"
            description="Must not overlap evt.>, _sys.>, $… or _INBOX.>"
            placeholder="orders.> (Enter to add)"
            required
            {...form.getInputProps('subjects')}
          />
          <SimpleGrid cols={3} spacing="sm">
            <TextInput label="Max bytes" description="e.g. 10GiB, unlimited" {...form.getInputProps('maxBytes')} />
            <TextInput label="Max age" description="e.g. 30d; empty = forever" {...form.getInputProps('maxAge')} />
            <NumberInput label="Max messages" description="empty = unlimited" min={1} allowDecimal={false} {...form.getInputProps('maxMsgs')} />
            <Select
              label="When full"
              data={[
                { value: 'old', label: 'Discard oldest' },
                { value: 'new', label: 'Reject new' },
              ]}
              allowDeselect={false}
              {...form.getInputProps('discard')}
            />
            <NumberInput label="Dedupe window (s)" min={0} max={86400} {...form.getInputProps('duplicates')} />
          </SimpleGrid>
          <Group>
            <Switch label="S2 compression" {...form.getInputProps('compression', { type: 'checkbox' })} />
            <Switch label="Allow direct get" {...form.getInputProps('allowDirect', { type: 'checkbox' })} />
          </Group>
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" loading={create.isPending || update.isPending}>
              {editing ? 'Save' : 'Create'}
            </Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  )
}
