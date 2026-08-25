// useWhoami: the operator's effective identity + permissions, shared via
// the ['whoami'] query key (the tenant switcher and the Admin page use the
// same key, so this resolves once per session and stays cached).
import { useQuery } from '@tanstack/react-query'
import { get } from '../api'
import type { Whoami } from '../types'

export function useWhoami() {
  return useQuery({
    queryKey: ['whoami'],
    queryFn: () => get<Whoami>('/whoami'),
    staleTime: 5 * 60_000,
  })
}
