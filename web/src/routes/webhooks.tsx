import { createFileRoute } from '@tanstack/react-router'
import { Placeholder } from '@/components/Placeholder'

export const Route = createFileRoute('/webhooks')({
  component: () => <Placeholder title="Webhooks" milestone="M7" />,
})
