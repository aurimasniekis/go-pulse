import { createFileRoute } from '@tanstack/react-router'
import { Placeholder } from '@/components/Placeholder'

export const Route = createFileRoute('/backups')({
  component: () => <Placeholder title="Backups" milestone="M12" />,
})
