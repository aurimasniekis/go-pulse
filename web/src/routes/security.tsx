import { createFileRoute } from '@tanstack/react-router'
import { Placeholder } from '@/components/Placeholder'

export const Route = createFileRoute('/security')({
  component: () => <Placeholder title="Security" milestone="M8" />,
})
