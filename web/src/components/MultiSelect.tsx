// MultiSelect: compact chips + typeahead combobox for multi-value reference
// fields (contacts, contact-groups, templates, parents). Drop-in for
// DualListPicker's {value,onChange,options,allowCustom} API but sized for a
// form: a single-line trigger opens a searchable dropdown, chosen entries show
// as removable chips. The full two-pane DualListPicker is reserved for wide
// surfaces (the template editor); two of these side-by-side stay usable where
// two dual-lists collapse to ~130px each (FORM-2).
import { useMemo, useState } from 'react'
import { Check, ChevronsUpDown, X, Plus } from 'lucide-react'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import {
  Command, CommandInput, CommandList, CommandEmpty, CommandGroup, CommandItem,
} from '@/components/ui/command'
import { t } from '../i18n'

export function MultiSelect({
  value, onChange, options, allowCustom = true, placeholder,
}: {
  value: string[]
  onChange: (v: string[]) => void
  options: string[]
  allowCustom?: boolean
  placeholder?: string
}) {
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')
  // Coerce + de-dupe (a shared query cache can resolve to the wrong shape).
  const opts = useMemo(() => Array.from(new Set((options ?? []).map(String))), [options])
  const selectedSet = useMemo(() => new Set(value), [value])

  const toggle = (item: string) =>
    onChange(selectedSet.has(item) ? value.filter((v) => v !== item) : [...value, item])
  const remove = (item: string) => onChange(value.filter((v) => v !== item))

  const q = query.trim()
  const canAdd = allowCustom && q !== '' && !opts.includes(q) && !selectedSet.has(q)

  return (
    <div className="space-y-1.5">
      {value.length > 0 && (
        <div className="flex flex-wrap gap-1">
          {value.map((v) => (
            <span key={v} className="inline-flex items-center gap-1 text-xs bg-muted text-foreground rounded-md px-2 py-1 font-mono">
              {v}
              <button
                type="button" aria-label={t('remove')}
                className="text-muted-foreground hover:text-danger cursor-pointer"
                onClick={() => remove(v)}
              ><X size={12} /></button>
            </span>
          ))}
        </div>
      )}

      <Popover open={open} onOpenChange={setOpen}>
        <PopoverTrigger asChild>
          <Button
            type="button" variant="outline" role="combobox" aria-expanded={open}
            aria-label={placeholder ?? t('add')}
            className="w-full justify-between h-8 font-normal text-muted-foreground"
          >
            {value.length ? `${value.length} ${t('selected')}` : (placeholder ?? t('add'))}
            <ChevronsUpDown size={14} className="opacity-50 shrink-0" />
          </Button>
        </PopoverTrigger>
        <PopoverContent className="w-[var(--radix-popover-trigger-width)] p-0" align="start">
          <Command>
            <CommandInput value={query} onValueChange={setQuery} placeholder={t('filterEllipsis')} className="h-9" />
            <CommandList>
              <CommandEmpty>{canAdd ? null : t('nothingFound')}</CommandEmpty>
              {canAdd && (
                <CommandGroup>
                  <CommandItem value={q} onSelect={() => { onChange([...value, q]); setQuery('') }}>
                    <Plus size={14} className="mr-2" />
                    {t('addValue')}: <span className="font-mono ml-1">{q}</span>
                  </CommandItem>
                </CommandGroup>
              )}
              <CommandGroup>
                {opts.map((o) => (
                  <CommandItem key={o} value={o} onSelect={() => toggle(o)}>
                    <Check size={14} className={cn('mr-2', selectedSet.has(o) ? 'opacity-100' : 'opacity-0')} />
                    <span className="font-mono truncate">{o}</span>
                  </CommandItem>
                ))}
              </CommandGroup>
            </CommandList>
          </Command>
        </PopoverContent>
      </Popover>
    </div>
  )
}
