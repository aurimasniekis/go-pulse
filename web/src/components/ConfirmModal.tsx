import { Button, Group, Modal, Stack, Text, TextInput } from '@mantine/core'
import { useState, type ReactNode } from 'react'

interface Props {
  opened: boolean
  onClose: () => void
  onConfirm: () => void
  title: string
  /** The user must type this to enable the button. */
  confirmText: string
  children?: ReactNode
  action?: string
  loading?: boolean
}

/** Destructive confirmation that requires typing the target name. */
export function ConfirmModal({ opened, onClose, onConfirm, title, confirmText, children, action = 'Delete', loading }: Props) {
  const [typed, setTyped] = useState('')
  const close = () => {
    setTyped('')
    onClose()
  }
  return (
    <Modal opened={opened} onClose={close} title={title} size="md">
      <Stack gap="sm">
        {children}
        <TextInput
          label={
            <Text size="xs" span>
              Type <b>{confirmText}</b> to confirm
            </Text>
          }
          value={typed}
          onChange={(e) => setTyped(e.currentTarget.value)}
          data-autofocus
          aria-label="confirmation"
        />
        <Group justify="flex-end">
          <Button variant="default" onClick={close}>
            Cancel
          </Button>
          <Button color="red" disabled={typed !== confirmText} loading={loading} onClick={onConfirm}>
            {action}
          </Button>
        </Group>
      </Stack>
    </Modal>
  )
}
