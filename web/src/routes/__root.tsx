import { Center, Stack, Text, Title } from '@mantine/core'
import type { QueryClient } from '@tanstack/react-query'
import { createRootRouteWithContext, Outlet } from '@tanstack/react-router'
import { lazy, Suspense } from 'react'
import { AppLayout } from '@/components/AppLayout'

const RouterDevtools = import.meta.env.DEV
  ? lazy(() => import('@tanstack/react-router-devtools').then((m) => ({ default: m.TanStackRouterDevtools })))
  : () => null

export const Route = createRootRouteWithContext<{ queryClient: QueryClient }>()({
  component: Root,
  notFoundComponent: NotFound,
})

function Root() {
  return (
    <AppLayout>
      <Outlet />
      <Suspense fallback={null}>
        <RouterDevtools position="bottom-right" />
      </Suspense>
    </AppLayout>
  )
}

function NotFound() {
  return (
    <Center h={300}>
      <Stack gap={4} align="center">
        <Title order={3}>Not found</Title>
        <Text c="dimmed" size="sm">This page does not exist.</Text>
      </Stack>
    </Center>
  )
}
