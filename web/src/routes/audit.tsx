import { createFileRoute } from '@tanstack/react-router'
import { Placeholder } from '@/components/Placeholder'

export const Route = createFileRoute('/audit')({
  component: () => <Placeholder title="Audit" milestone="M12" />,
})
