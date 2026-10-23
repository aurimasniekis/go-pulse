import { notifications } from '@mantine/notifications'

export const notifyOk = (message: string) => notifications.show({ color: 'teal', message, autoClose: 3000 })
export const notifyErr = (err: unknown) =>
  notifications.show({ color: 'red', title: 'Error', message: err instanceof Error ? err.message : String(err), autoClose: 6000 })
