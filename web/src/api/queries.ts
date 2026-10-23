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
