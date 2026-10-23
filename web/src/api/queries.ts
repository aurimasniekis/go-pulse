import { keepPreviousData, useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { admin, unwrap, type EventsPage, type Schemas } from './client'

/** Query parameters of GET /events, as kept in the explorer URL. */
export interface EventsQuery {
  stream?: string
  subject?: string[]
  dir?: 'forward' | 'backward'
  start_seq?: number
  start_time?: string
  limit?: number
  type?: string
  header?: string[]
  contains?: string
  json_path?: string
  json_value?: string
}

export const qk = {
  system: ['system'] as const,
  streams: ['streams'] as const,
  stream: (name: string) => ['streams', name] as const,
  consumers: (name: string) => ['streams', name, 'consumers'] as const,
  events: (q: EventsQuery) => ['events', q] as const,
  overview: (stream: string) => ['overview', stream] as const,
  throughput: (stream: string, range: string, dim: string) => ['throughput', stream, range, dim] as const,
  tree: (stream: string, prefix: string) => ['subjects', stream, prefix] as const,
  subject: (stream: string, subject: string) => ['subject', stream, subject] as const,
}

export function useSystemInfo() {
  return useQuery({
    queryKey: qk.system,
    queryFn: async () => unwrap(await admin.GET('/system/info')),
    refetchInterval: 10_000,
    retry: false,
  })
}

export function useStreams() {
  return useQuery({ queryKey: qk.streams, queryFn: async () => unwrap(await admin.GET('/streams')), refetchInterval: 10_000 })
}

export function useStream(name: string) {
  return useQuery({
    queryKey: qk.stream(name),
    queryFn: async () => unwrap(await admin.GET('/streams/{name}', { params: { path: { name } } })),
    refetchInterval: 10_000,
  })
}

export function useConsumers(name: string) {
  return useQuery({
    queryKey: qk.consumers(name),
    queryFn: async () => unwrap(await admin.GET('/streams/{name}/consumers', { params: { path: { name } } })),
    refetchInterval: 10_000,
  })
}

export function useEvents(q: EventsQuery) {
  return useInfiniteQuery({
    queryKey: qk.events(q),
    initialPageParam: undefined as string | undefined,
    queryFn: async ({ pageParam }): Promise<EventsPage> =>
      unwrap(await admin.GET('/events', { params: { query: { ...q, cursor: pageParam } } })),
    getNextPageParam: (last) => last.next || undefined,
    placeholderData: keepPreviousData,
    staleTime: 0,
  })
}

export type StreamInput = Schemas['StreamInput']

export function useStreamMutations() {
  const qc = useQueryClient()
  const invalidate = () => qc.invalidateQueries({ queryKey: qk.streams })
  return {
    create: useMutation({
      mutationFn: async (body: StreamInput) => unwrap(await admin.POST('/streams', { body })),
      onSuccess: invalidate,
    }),
    update: useMutation({
      mutationFn: async (body: StreamInput) => unwrap(await admin.PUT('/streams/{name}', { params: { path: { name: body.name } }, body })),
      onSuccess: invalidate,
    }),
    remove: useMutation({
      mutationFn: async (name: string) => {
        const res = await admin.DELETE('/streams/{name}', { params: { path: { name }, query: { confirm: name } } })
        if (res.error) unwrap(res)
      },
      onSuccess: invalidate,
    }),
    removeConsumer: useMutation({
      mutationFn: async ({ stream, consumer }: { stream: string; consumer: string }) => {
        const res = await admin.DELETE('/streams/{name}/consumers/{consumer}', {
          params: { path: { name: stream, consumer }, query: { confirm: consumer } },
        })
        if (res.error) unwrap(res)
      },
      onSuccess: (_d, v) => qc.invalidateQueries({ queryKey: qk.consumers(v.stream) }),
    }),
  }
}

export function useOverview(stream = 'EVENTS') {
  return useQuery({
    queryKey: qk.overview(stream),
    queryFn: async () => unwrap(await admin.GET('/overview', { params: { query: { stream } } })),
    refetchInterval: 15_000,
  })
}

export function useThroughput(stream: string, range: '1h' | '24h' | '7d' | '30d' | '1y', dim: 'total' | 'type' | 'p1' | 'p2' | 'p3' | 'p4') {
  return useQuery({
    queryKey: qk.throughput(stream, range, dim),
    queryFn: async () => unwrap(await admin.GET('/throughput', { params: { query: { stream, range, dim, top: 6 } } })),
    refetchInterval: range === '1h' ? 15_000 : 60_000,
    placeholderData: keepPreviousData,
  })
}

export function useSubjectTree(stream: string, prefix: string, enabled = true) {
  return useQuery({
    queryKey: qk.tree(stream, prefix),
    queryFn: async () => unwrap(await admin.GET('/subjects/tree', { params: { query: { stream, prefix } } })),
    enabled,
    staleTime: 15_000,
  })
}

export function useSubjectInfo(stream: string, subject: string | null) {
  return useQuery({
    queryKey: qk.subject(stream, subject ?? ''),
    queryFn: async () => unwrap(await admin.GET('/subjects/info', { params: { query: { stream, subject: subject ?? '' } } })),
    enabled: !!subject,
  })
}

export function useClients() {
  return useQuery({ queryKey: ['clients'], queryFn: async () => unwrap(await admin.GET('/clients')), refetchInterval: 15_000 })
}

export function useClient(id: string) {
  return useQuery({
    queryKey: ['clients', id],
    queryFn: async () => unwrap(await admin.GET('/clients/{id}', { params: { path: { id } } })),
    refetchInterval: 15_000,
  })
}

export function useClientConnections(id: string, enabled: boolean) {
  return useQuery({
    queryKey: ['clients', id, 'connections'],
    queryFn: async () => unwrap(await admin.GET('/clients/{id}/connections', { params: { path: { id } } })),
    refetchInterval: 5_000,
    enabled,
  })
}

export function useTokens() {
  return useQuery({ queryKey: ['tokens'], queryFn: async () => unwrap(await admin.GET('/tokens')), refetchInterval: 30_000 })
}

export interface AuditQuery {
  action?: string
  actor?: string
  target?: string
  result?: 'ok' | 'denied' | 'error'
}

export function useAudit(q: AuditQuery) {
  return useInfiniteQuery({
    queryKey: ['audit', q],
    initialPageParam: 0,
    queryFn: async ({ pageParam }) =>
      unwrap(await admin.GET('/audit', { params: { query: { ...q, limit: 100, before_id: pageParam || undefined } } })),
    getNextPageParam: (last) => last.next || undefined,
  })
}

export function useInboundHooks() {
  return useQuery({ queryKey: ['hooks', 'in'], queryFn: async () => unwrap(await admin.GET('/hooks/inbound')), refetchInterval: 15_000 })
}

export function useOutboundHooks() {
  return useQuery({ queryKey: ['hooks', 'out'], queryFn: async () => unwrap(await admin.GET('/hooks/outbound')), refetchInterval: 10_000 })
}

export function useDeliveries(id: string | null) {
  return useQuery({
    queryKey: ['hooks', 'out', id, 'deliveries'],
    queryFn: async () => unwrap(await admin.GET('/hooks/outbound/{id}/deliveries', { params: { path: { id: id! }, query: { limit: 200 } } })),
    enabled: !!id,
    refetchInterval: 5_000,
  })
}

export function useSecurity() {
  return useQuery({ queryKey: ['security'], queryFn: async () => unwrap(await admin.GET('/security')), refetchInterval: 5_000 })
}

export function useCertificates() {
  return useQuery({ queryKey: ['certificates'], queryFn: async () => unwrap(await admin.GET('/system/certificates')), refetchInterval: 30_000 })
}
