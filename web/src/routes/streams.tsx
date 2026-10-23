import { createFileRoute } from '@tanstack/react-router'
import { Placeholder } from '@/components/Placeholder'

export const Route = createFileRoute('/streams')({
  component: () => <Placeholder title="Streams" milestone="M4" />,
})
