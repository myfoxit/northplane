// Humanise raw SNMP check output for display. A default SNMP uptime check
// renders its result verbatim as e.g. "VALUE OK - 1.3.6.1.2.1.1.3.0 = 20478192"
// — a raw OID and a TimeTicks counter (hundredths of a second) that means
// nothing to a human. We rewrite the OID=ticks fragment into "sysUpTime: up 2d
// 8h" while leaving the rest of the line (the "VALUE OK -" status) intact.
// (NP-19)

// sysUpTime.0 — the standard SNMPv2-MIB uptime object, in TimeTicks.
const SYS_UPTIME_OID = '1.3.6.1.2.1.1.3.0'

// formatUptimeTicks turns SNMP TimeTicks (centiseconds) into a compact
// "up 2d 8h 13m" string. The two most-significant non-zero units are kept so
// the label stays short; sub-minute uptimes fall back to seconds.
export function formatUptimeTicks(ticks: number): string {
  if (!Number.isFinite(ticks) || ticks < 0) return ''
  const totalSec = Math.floor(ticks / 100)
  const d = Math.floor(totalSec / 86400)
  const h = Math.floor((totalSec % 86400) / 3600)
  const m = Math.floor((totalSec % 3600) / 60)
  const s = totalSec % 60
  const parts: [number, string][] = [[d, 'd'], [h, 'h'], [m, 'm'], [s, 's']]
  const nonZero = parts.filter(([n]) => n > 0)
  const shown = (nonZero.length ? nonZero : [[s, 's'] as [number, string]]).slice(0, 2)
  return `up ${shown.map(([n, u]) => `${n}${u}`).join(' ')}`
}

const uptimeRe = new RegExp(`${SYS_UPTIME_OID.replace(/\./g, '\\.')}\\s*=\\s*(\\d+)`)

// humanizeOutput rewrites a sysUpTime OID reading inside a check-output string
// into a human uptime. Any other output is returned unchanged.
export function humanizeOutput(output?: string | null): string {
  if (!output) return output ?? ''
  const m = uptimeRe.exec(output)
  if (!m) return output
  const human = formatUptimeTicks(Number(m[1]))
  if (!human) return output
  return output.slice(0, m.index) + `sysUpTime: ${human}` + output.slice(m.index + m[0].length)
}
