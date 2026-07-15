// Redact secret-bearing values before rendering object/effective config to the
// UI. The agent credential in particular shows up in ObjectSpec.args as
// ["--token", "nlagent-…"] or "--token=nlagent-…", and an object's effective
// config is viewable by anyone who can see the object — so the token must never
// render inline (DETAIL-1). Reused by the raw-JSON dump and the formatted
// effective-config table.

export const REDACTED = '•••'

// Normalised secret words: a key/flag name is treated as secret if, once
// stripped to [a-z0-9], it contains one of these. Kept conservative so ops
// macros like $_HOSTKEY$ / serviceKey are NOT masked ('key' alone is not here).
const SECRET_WORDS = [
  'token', 'password', 'passwd', 'pwd', 'secret', 'apikey',
  'credential', 'passphrase', 'privatekey',
]

// isSecretName: does this key or CLI flag name denote a secret? Compares the
// alphanumeric-only, lowercased form so "--api-key", "apiKey" and "API_KEY"
// all normalise to "apikey".
export function isSecretName(raw: string): boolean {
  const norm = raw.toLowerCase().replace(/[^a-z0-9]/g, '')
  if (!norm) return false
  return SECRET_WORDS.some((w) => norm.includes(w))
}

const isFlag = (s: string) => s.startsWith('-')

// redactArray masks CLI-style arg lists: "--token=value" keeps the flag but
// masks the value; a bare "--token" flag masks the following value element.
function redactArray(arr: unknown[]): unknown[] {
  const out: unknown[] = []
  for (let i = 0; i < arr.length; i++) {
    const el = arr[i]
    if (typeof el !== 'string') {
      out.push(redactSecrets(el))
      continue
    }
    const eq = el.indexOf('=')
    if (eq > 0 && isSecretName(el.slice(0, eq))) {
      out.push(`${el.slice(0, eq)}=${REDACTED}`)
      continue
    }
    const next = arr[i + 1]
    if (isFlag(el) && isSecretName(el) && typeof next === 'string' && !isFlag(next)) {
      out.push(el, REDACTED)
      i++
      continue
    }
    out.push(el)
  }
  return out
}

// redactSecrets deep-clones `value`, masking any object value whose key is a
// secret name and any secret arg inside a string array. Non-secret data is
// returned structurally unchanged.
export function redactSecrets(value: unknown): unknown {
  if (Array.isArray(value)) return redactArray(value)
  if (value && typeof value === 'object') {
    const out: Record<string, unknown> = {}
    for (const [k, v] of Object.entries(value)) {
      out[k] = isSecretName(k) ? REDACTED : redactSecrets(v)
    }
    return out
  }
  return value
}
