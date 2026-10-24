import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { meKey } from './auth'
import { admin, check, unwrap, type ProviderInput, type Schemas } from './client'

/** Queries and mutations for the account, users, invitations and single sign-on (ADR-0038). */

export function useMySessions() {
  return useQuery({ queryKey: ['account', 'sessions'], queryFn: async () => unwrap(await admin.GET('/account/sessions')), refetchInterval: 30_000 })
}

export function useMyIdentities() {
  return useQuery({ queryKey: ['account', 'identities'], queryFn: async () => unwrap(await admin.GET('/account/identities')) })
}

export function useMyTokens() {
  return useQuery({ queryKey: ['account', 'tokens'], queryFn: async () => unwrap(await admin.GET('/account/tokens')) })
}

export function useAccountMutations() {
  const qc = useQueryClient()
  const refresh = (key: string) => () => {
    void qc.invalidateQueries({ queryKey: ['account', key] })
    void qc.invalidateQueries({ queryKey: meKey })
  }
  return {
    revokeSession: useMutation({
      mutationFn: async (id: string) => check(await admin.DELETE('/account/sessions/{id}', { params: { path: { id } } })),
      onSuccess: refresh('sessions'),
    }),
    changePassword: useMutation({
      mutationFn: async (body: { current_password: string; new_password: string }) => check(await admin.POST('/account/password', { body })),
      onSuccess: refresh('sessions'),
    }),
    disableTotp: useMutation({
      mutationFn: async () => check(await admin.DELETE('/account/totp')),
      onSuccess: refresh('sessions'),
    }),
    regenerateCodes: useMutation({
      mutationFn: async () => unwrap(await admin.POST('/account/recovery-codes')).recovery_codes,
      onSuccess: refresh('sessions'),
    }),
    createToken: useMutation({
      mutationFn: async (body: { name: string; role: Schemas['PAT']['role']; days?: number }) =>
        unwrap(await admin.POST('/account/tokens', { body: body as never })),
      onSuccess: refresh('tokens'),
    }),
    revokeToken: useMutation({
      mutationFn: async (id: string) => check(await admin.DELETE('/account/tokens/{id}', { params: { path: { id } } })),
      onSuccess: refresh('tokens'),
    }),
  }
}

export function useUsers() {
  return useQuery({ queryKey: ['users'], queryFn: async () => unwrap(await admin.GET('/users')), refetchInterval: 30_000 })
}

export function useUser(id: string) {
  return useQuery({ queryKey: ['users', id], queryFn: async () => unwrap(await admin.GET('/users/{id}', { params: { path: { id } } })) })
}

export function useUserSessions(id: string) {
  return useQuery({
    queryKey: ['users', id, 'sessions'],
    queryFn: async () => unwrap(await admin.GET('/users/{id}/sessions', { params: { path: { id } } })),
    refetchInterval: 30_000,
  })
}

export function useUserIdentities(id: string) {
  return useQuery({
    queryKey: ['users', id, 'identities'],
    queryFn: async () => unwrap(await admin.GET('/users/{id}/identities', { params: { path: { id } } })),
  })
}

export function useUserTokens(id: string) {
  return useQuery({
    queryKey: ['users', id, 'tokens'],
    queryFn: async () => unwrap(await admin.GET('/users/{id}/tokens', { params: { path: { id } } })),
  })
}

export function useInvitations(enabled = true) {
  return useQuery({ queryKey: ['invitations'], queryFn: async () => unwrap(await admin.GET('/invitations')), enabled })
}

export function useUserMutations() {
  const qc = useQueryClient()
  const users = () => void qc.invalidateQueries({ queryKey: ['users'] })
  const invitations = () => void qc.invalidateQueries({ queryKey: ['invitations'] })
  return {
    create: useMutation({
      mutationFn: async (body: Schemas['CreateInput']) => unwrap(await admin.POST('/users', { body })),
      onSuccess: users,
    }),
    update: useMutation({
      mutationFn: async ({ id, body }: { id: string; body: Schemas['UpdateInput'] }) =>
        unwrap(await admin.PATCH('/users/{id}', { params: { path: { id } }, body })),
      onSuccess: users,
    }),
    remove: useMutation({
      mutationFn: async (id: string) => check(await admin.DELETE('/users/{id}', { params: { path: { id } } })),
      onSuccess: users,
    }),
    resetLink: useMutation({
      mutationFn: async (id: string) => unwrap(await admin.POST('/users/{id}/reset-link', { params: { path: { id } } })),
    }),
    revokeSessions: useMutation({
      mutationFn: async (id: string) => check(await admin.DELETE('/users/{id}/sessions', { params: { path: { id } } })),
      onSuccess: users,
    }),
    clearThrottle: useMutation({
      mutationFn: async (id: string) => check(await admin.POST('/users/{id}/clear-throttle', { params: { path: { id } } })),
    }),
    revokeToken: useMutation({
      mutationFn: async ({ id, token }: { id: string; token: string }) =>
        check(await admin.DELETE('/users/{id}/tokens/{token}', { params: { path: { id, token } } })),
      onSuccess: users,
    }),
    linkIdentity: useMutation({
      mutationFn: async ({ id, provider_id, subject }: { id: string; provider_id: string; subject: string }) =>
        check(await admin.POST('/users/{id}/identities', { params: { path: { id } }, body: { provider_id, subject } })),
      onSuccess: users,
    }),
    unlinkIdentity: useMutation({
      mutationFn: async ({ id, issuer, subject }: { id: string; issuer: string; subject: string }) =>
        check(await admin.DELETE('/users/{id}/identities', { params: { path: { id }, query: { issuer, subject } } })),
      onSuccess: users,
    }),
    linkInvite: useMutation({
      mutationFn: async ({ id, provider_id }: { id: string; provider_id: string }) =>
        unwrap(await admin.POST('/users/{id}/link-invite', { params: { path: { id } }, body: { provider_id } })),
    }),
    invite: useMutation({
      mutationFn: async (body: { email: string; roles: string[] }) => unwrap(await admin.POST('/invitations', { body })),
      onSuccess: invitations,
    }),
    revokeInvitation: useMutation({
      mutationFn: async (id: string) => check(await admin.DELETE('/invitations/{id}', { params: { path: { id } } })),
      onSuccess: invitations,
    }),
  }
}

export function useSSOProviders(enabled = true) {
  return useQuery({ queryKey: ['sso', 'providers'], queryFn: async () => unwrap(await admin.GET('/sso/providers')), enabled })
}

export function useSSOMappings(id: string | null) {
  return useQuery({
    queryKey: ['sso', 'providers', id, 'mappings'],
    enabled: !!id,
    queryFn: async () => unwrap(await admin.GET('/sso/providers/{id}/mappings', { params: { path: { id: id! } } })),
  })
}

export function useSSOSettings(enabled = true) {
  return useQuery({ queryKey: ['sso', 'settings'], queryFn: async () => unwrap(await admin.GET('/sso/settings')), enabled })
}

export function useSSOTestResult(id: string | undefined) {
  return useQuery({
    queryKey: ['sso', 'test', id],
    enabled: !!id,
    retry: false,
    queryFn: async () => unwrap(await admin.GET('/sso/providers/{id}/test-result', { params: { path: { id: id! } } })),
  })
}

export function useSSOMutations() {
  const qc = useQueryClient()
  const sso = () => void qc.invalidateQueries({ queryKey: ['sso'] })
  return {
    create: useMutation({
      mutationFn: async (body: ProviderInput) => unwrap(await admin.POST('/sso/providers', { body })),
      onSuccess: sso,
    }),
    update: useMutation({
      mutationFn: async ({ id, body }: { id: string; body: ProviderInput }) =>
        unwrap(await admin.PUT('/sso/providers/{id}', { params: { path: { id } }, body })),
      onSuccess: sso,
    }),
    remove: useMutation({
      mutationFn: async (id: string) => check(await admin.DELETE('/sso/providers/{id}', { params: { path: { id } } })),
      onSuccess: sso,
    }),
    addMapping: useMutation({
      mutationFn: async ({ id, group_value, role }: { id: string; group_value: string; role: string }) =>
        unwrap(await admin.POST('/sso/providers/{id}/mappings', { params: { path: { id } }, body: { group_value, role: role as never } })),
      onSuccess: sso,
    }),
    deleteMapping: useMutation({
      mutationFn: async ({ id, mapping }: { id: string; mapping: string }) =>
        check(await admin.DELETE('/sso/providers/{id}/mappings/{mapping}', { params: { path: { id, mapping } } })),
      onSuccess: sso,
    }),
    setLocalLoginDisabled: useMutation({
      mutationFn: async (disabled: boolean) => unwrap(await admin.PUT('/sso/settings', { body: { local_login_disabled: disabled } })),
      onSuccess: () => {
        sso()
        void qc.invalidateQueries({ queryKey: ['auth-config'] })
      },
    }),
  }
}
