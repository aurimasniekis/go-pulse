import { createFileRoute } from '@tanstack/react-router'
import { Placeholder } from '@/components/Placeholder'

export const Route = createFileRoute('/tokens')({
  component: () => <Placeholder title="HTTP tokens" milestone="M6" />,
})
