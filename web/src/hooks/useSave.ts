// useSave: uniform mutation wrapper — invalidates query keys, surfaces
// the APIError for FormError, runs onDone (e.g. closes the dialog) on success.
import { useMutation, useQueryClient } from '@tanstack/react-query'

export function useSave<TArgs>(fn: (args: TArgs) => Promise<unknown>, opts: {
  invalidate: readonly (readonly string[])[]; onDone?: () => void
}) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: fn,
    onSuccess: () => {
      for (const key of opts.invalidate) qc.invalidateQueries({ queryKey: key as string[] })
      opts.onDone?.()
    },
  })
}
