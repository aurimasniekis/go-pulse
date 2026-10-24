import { Alert, Button, Center, Loader, Stack, Text, Title } from '@mantine/core'
import type { QueryClient } from '@tanstack/react-query'
import { createRootRouteWithContext, Outlet, useRouterState } from '@tanstack/react-router'
import { lazy, Suspense, useEffect, type ReactNode } from 'react'
import { useMe } from '@/api/auth'
import { isPublicPath, loginUrl } from '@/api/session'
import { AppLayout } from '@/components/AppLayout'
import { errorMessage } from '@/features/auth/errors'
import { ReauthModal } from '@/features/auth/ReauthModal'

const RouterDevtools = import.meta.env.DEV
  ? lazy(() => import('@tanstack/react-router-devtools').then((m) => ({ default: m.TanStackRouterDevtools })))
  : () => null

export const Route = createRootRouteWithContext<{ queryClient: QueryClient }>()({
  component: Root,
  notFoundComponent: NotFound,
})

function Root() {
  const path = useRouterState({ select: (s) => s.location.pathname })
  const devtools = (
    <Suspense fallback={null}>
      <RouterDevtools position="bottom-right" />
    </Suspense>
  )
  if (isPublicPath(path)) {
    return (
      <>
        <Outlet />
        {devtools}
      </>
    )
  }
  return (
    <AuthGate>
      <AppLayout>
        <Outlet />
        {devtools}
      </AppLayout>
      <ReauthModal />
    </AuthGate>
  )
}

/** Every page except sign-in needs a complete session (ADR-0038); otherwise go to /login and come back. */
function AuthGate({ children }: { children: ReactNode }) {
  const me = useMe()
  const location = useRouterState({ select: (s) => s.location })
  const incomplete = me.isSuccess && (!me.data || me.data.mfa_pending || me.data.must_enroll)
  useEffect(() => {
    if (incomplete) window.location.assign(loginUrl(location.href))
  }, [incomplete, location.href])
  if (me.isError) {
    return (
      <Center h="100vh" p="md">
        <Alert color="red" title="Pulse is unreachable" maw={480}>
          <Stack gap="xs" align="flex-start">
            <Text size="sm">{errorMessage(me.error)}</Text>
            <Button size="xs" onClick={() => void me.refetch()}>
              Retry
            </Button>
          </Stack>
        </Alert>
      </Center>
    )
  }
  if (!me.isSuccess || incomplete) {
    return (
      <Center h="100vh">
        <Loader size="sm" />
      </Center>
    )
  }
  return <>{children}</>
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
