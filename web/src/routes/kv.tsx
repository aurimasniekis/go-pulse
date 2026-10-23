import { createFileRoute } from '@tanstack/react-router'
import { Placeholder } from '@/components/Placeholder'

export const Route = createFileRoute('/kv')({
  component: () => <Placeholder title="KV & Objects" milestone="M12" />,
})
