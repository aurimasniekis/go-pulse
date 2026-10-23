import { createFileRoute } from '@tanstack/react-router'
import { Placeholder } from '@/components/Placeholder'

export const Route = createFileRoute('/settings')({
  component: () => <Placeholder title="Settings" milestone="M12" />,
})
