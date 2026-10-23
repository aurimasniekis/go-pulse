import { createFileRoute } from '@tanstack/react-router'
import { Placeholder } from '@/components/Placeholder'

export const Route = createFileRoute('/explorer')({
  component: () => <Placeholder title="Explorer" milestone="M4" />,
})
