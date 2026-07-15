// Parse a Nagios/CMP perfdata string into structured metrics so the object
// detail view can render mini-meters instead of a raw string like
// "load1=1.09;3;6;;" (DETAIL-5). Format (space-separated, label may be quoted):
//   'label'=value[UOM];warn;crit;min;max
// Any trailing field may be empty; warn/crit may be Nagios ranges (we keep the
// first number, enough to draw a threshold marker).
export interface Perf {
  label: string
  value: number
  unit: string
  warn: number | null
  crit: number | null
  min: number | null
  max: number | null
}

const num = (s: string | undefined): number | null => {
  if (s === undefined || s === '') return null
  const v = parseFloat(s) // parseFloat("10:20") → 10 (range lower bound is fine for a marker)
  return Number.isFinite(v) ? v : null
}

export function parsePerfdata(raw?: string): Perf[] {
  if (!raw) return []
  const out: Perf[] = []
  // Tokens are space-separated, but a quoted label may itself contain spaces.
  const tokens = raw.match(/(?:'[^']*'|\S)+/g) ?? []
  for (const tok of tokens) {
    const eq = tok.lastIndexOf('=')
    if (eq <= 0) continue
    let label = tok.slice(0, eq).trim()
    if (label.startsWith("'") && label.endsWith("'")) label = label.slice(1, -1)
    const [valPart, warn, crit, min, max] = tok.slice(eq + 1).split(';')
    const m = /^(-?[0-9.]+)(.*)$/.exec(valPart ?? '')
    if (!m) continue
    const value = parseFloat(m[1]!)
    if (!Number.isFinite(value)) continue
    out.push({
      label,
      value,
      unit: (m[2] ?? '').trim(),
      warn: num(warn), crit: num(crit), min: num(min), max: num(max),
    })
  }
  return out
}
