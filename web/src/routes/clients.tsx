import { createFileRoute } from '@tanstack/react-router'
import { Placeholder } from '@/components/Placeholder'

export const Route = createFileRoute('/clients')({
  component: () => <Placeholder title="Clients" milestone="M6" />,
})
