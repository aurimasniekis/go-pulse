import { Button, Group, Modal, Select, Stack, TagsInput, Textarea, TextInput } from '@mantine/core'
import { useForm } from '@mantine/form'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { admin, unwrap } from '@/api/client'
import { notifyErr, notifyOk } from '@/lib/notify'

const subjectRe = /^evt(\.[a-z0-9_-]+){3,}$/

/** Publishes one event as the operator (audited); handy for testing consumers and webhooks. */
export function PublishModal({ opened, onClose }: { opened: boolean; onClose: () => void }) {
  const qc = useQueryClient()
  const form = useForm({
    initialValues: { subject: '', type: '', contentType: 'application/json', data: '{}', headers: [] as string[] },
    validate: {
      subject: (v) => (subjectRe.test(v) ? null : 'evt.<domain>.<entity>.<action>[.<key>], lowercase'),
      data: (v, values) => {
        if (values.contentType !== 'application/json' || v.trim() === '') return null
        try {
          JSON.parse(v)
          return null
        } catch {
          return 'Not valid JSON'
        }
      },
      headers: (v) => (v.every((h) => /^[^=\s]+=.*$/.test(h)) ? null : 'Headers are key=value'),
    },
  })
  const publish = useMutation({
    mutationFn: async (v: typeof form.values) =>
      unwrap(
        await admin.POST('/events', {
          body: {
            subject: v.subject,
            type: v.type || undefined,
            content_type: v.contentType,
            data: v.data,
            headers: Object.fromEntries(v.headers.map((h) => [h.slice(0, h.indexOf('=')), h.slice(h.indexOf('=') + 1)])),
          },
        }),
      ),
    onSuccess: (r) => {
      notifyOk(`Published ${r.duplicate ? '(duplicate) ' : ''}seq ${r.seq}`)
      void qc.invalidateQueries({ queryKey: ['events'] })
      onClose()
    },
    onError: notifyErr,
  })
  return (
    <Modal opened={opened} onClose={onClose} title="Publish event" size="lg">
      <form onSubmit={form.onSubmit((v) => publish.mutate(v))}>
        <Stack gap="sm">
          <TextInput label="Subject" placeholder="evt.home.door.opened" required {...form.getInputProps('subject')} />
          <Group grow>
            <TextInput label="Type" placeholder="home.door.opened.v1 (empty = untyped)" {...form.getInputProps('type')} />
            <Select
              label="Content type"
              data={['application/json', 'text/plain; charset=utf-8']}
              allowDeselect={false}
              {...form.getInputProps('contentType')}
            />
          </Group>
          <Textarea label="Payload" autosize minRows={4} maxRows={14} ff="monospace" {...form.getInputProps('data')} />
          <TagsInput label="Extra headers" placeholder="key=value" {...form.getInputProps('headers')} />
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" loading={publish.isPending}>
              Publish
            </Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  )
}
