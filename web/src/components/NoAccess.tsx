import { Alert, Center, Stack, Text } from '@mantine/core'
import { IconLock } from '@tabler/icons-react'
import { useMe } from '@/api/auth'

/** Shown instead of a page the signed-in user's roles do not cover. */
export function NoAccess() {
  const me = useMe().data
  const noRole = (me?.user.roles.length ?? 0) === 0
  return (
    <Center h={300}>
      <Alert color="gray" icon={<IconLock size={16} />} title={noRole ? 'Your account has no role yet' : 'No access'} maw={480}>
        <Stack gap={4}>
          <Text size="sm">
            {noRole
              ? 'Ask an administrator to give you a role (or map one of your single sign-on groups to a role).'
              : 'Your role does not include this page. Ask an administrator if you need it.'}
          </Text>
        </Stack>
      </Alert>
    </Center>
  )
}
