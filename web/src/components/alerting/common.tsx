// Shared component widgets for the alerting-config UIs (datetime input,
// channel picker, severity field, toggle row). Pure helpers/constants live
// in datetime.ts and are re-exported here for a single import surface.
import { type ReactNode } from 'react'
import type { ChannelType, Severity } from '../../types'
import { Field, Toggle } from '../forms'
import { channelTypes, severities } from './datetime'

// A plain styled datetime-local input (forms.tsx has no dedicated one).
export function DateTimeInput({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  return (
    <input
      type="datetime-local"
      value={value}
      onChange={(e) => onChange(e.target.value)}
      className="bg-slate-900 border border-slate-700 rounded-lg px-3 py-1.5 text-sm text-slate-200 w-full focus:outline-none focus:border-blue-500"
    />
  )
}

// Multi-select of channel types as a checkbox grid.
export function ChannelPicker({ value, onChange }: {
  value: ChannelType[]; onChange: (v: ChannelType[]) => void
}) {
  const toggle = (c: ChannelType) =>
    onChange(value.includes(c) ? value.filter((x) => x !== c) : [...value, c])
  return (
    <div className="flex flex-wrap gap-1.5">
      {channelTypes.map((c) => {
        const on = value.includes(c)
        return (
          <button
            key={c} type="button" onClick={() => toggle(c)}
            className={`px-2 py-1 rounded-md text-xs font-medium border cursor-pointer transition-colors ${
              on ? 'bg-blue-600 border-blue-500 text-white'
                 : 'bg-slate-900 border-slate-700 text-slate-400 hover:text-slate-200'}`}
          >
            {c}
          </button>
        )
      })}
    </div>
  )
}

// A labelled severity <select> reused by rules and elsewhere.
export function SeverityField({ value, onChange, label }: {
  value: Severity; onChange: (v: Severity) => void; label: ReactNode
}) {
  return (
    <Field label={label}>
      <select
        value={value} onChange={(e) => onChange(e.target.value as Severity)}
        className="bg-slate-900 border border-slate-700 rounded-lg px-3 py-1.5 text-sm text-slate-200 w-full focus:outline-none focus:border-blue-500 cursor-pointer"
      >
        {severities.map((s) => <option key={s} value={s}>{s}</option>)}
      </select>
    </Field>
  )
}

// Inline labelled toggle row (Field renders the label above the control,
// which is awkward for switches — this keeps label beside the switch).
export function ToggleRow({ label, checked, onChange, hint }: {
  label: string; checked: boolean; onChange: (v: boolean) => void; hint?: string
}) {
  return (
    <div className="py-0.5">
      <Toggle checked={checked} onChange={onChange} label={label} />
      {hint && <div className="text-[11px] text-slate-500 mt-0.5">{hint}</div>}
    </div>
  )
}
