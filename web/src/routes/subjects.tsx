import { createFileRoute } from '@tanstack/react-router'
import { Placeholder } from '@/components/Placeholder'

export const Route = createFileRoute('/subjects')({
  component: () => <Placeholder title="Subjects" milestone="M5" />,
})
