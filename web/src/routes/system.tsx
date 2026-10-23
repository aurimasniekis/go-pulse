import { createFileRoute } from '@tanstack/react-router'
import { Placeholder } from '@/components/Placeholder'

export const Route = createFileRoute('/system')({
  component: () => <Placeholder title="System" milestone="M10" />,
})
