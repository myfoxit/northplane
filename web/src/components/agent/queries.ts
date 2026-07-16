// Shared query configs for the agent-chat feature.
import { get, type ListResponse } from '../../api'
import type { AIConnection, AIProviderType } from '../../types'

export const connectionsQuery = {
  queryKey: ['ai-connections'],
  queryFn: () => get<ListResponse<AIConnection>>('/ai/connections'),
}

export const providersQuery = {
  queryKey: ['ai-providers'],
  queryFn: () => get<ListResponse<AIProviderType>>('/ai/providers'),
  staleTime: 3_600_000,
}
