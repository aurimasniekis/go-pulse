import { createFileRoute } from '@tanstack/react-router'
import { Placeholder } from '@/components/Placeholder'

export const Route = createFileRoute('/tail')({
  component: () => <Placeholder title="Live tail" milestone="M4" />,
})
