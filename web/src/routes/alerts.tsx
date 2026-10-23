import { createFileRoute } from '@tanstack/react-router'
import { Placeholder } from '@/components/Placeholder'

export const Route = createFileRoute('/alerts')({
  component: () => <Placeholder title="Alerts" milestone="M12" />,
})
