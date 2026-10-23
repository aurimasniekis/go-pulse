import { Alert } from '@mantine/core'
import { IconHammer } from '@tabler/icons-react'
import { PageHeader } from './PageHeader'

// Shown for pages whose milestone has not landed yet (docs/PLAN.md §5).
export function Placeholder({ title, milestone }: { title: string; milestone: string }) {
  return (
    <>
      <PageHeader title={title} />
      <Alert icon={<IconHammer size={16} />} color="gray" title="Not built yet">
        This page arrives with milestone {milestone}.
      </Alert>
    </>
  )
}
