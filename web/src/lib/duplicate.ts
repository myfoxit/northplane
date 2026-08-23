// Duplicate helpers: turn a stored document into the seed for a "create"
// form ("Duplizieren" on lists and detail pages).
//
// Two things every copy needs, independent of the entity type:
//
// 1. The server envelope must NOT travel with the copy. Stored documents
//    carry id/tenantId/version/createdAt/updatedAt — and the store honours a
//    caller-set `id` on POST (storage.newDocID), so a naive `{...doc, name}`
//    would mint a second document with the SAME id as its source. stripMeta
//    removes exactly that envelope (plus any caller-named extras such as
//    `system` on roles).
//
// 2. A name that does not collide with the source. copyName appends a
//    `-copy` suffix to slug-like names (hosts, services, contacts, channels,
//    policies … — things that end up in selectors, URLs and check args, so no
//    whitespace) and the localised " (Kopie)" suffix to free-text names
//    (dashboards, reports). When the caller knows the existing names, the
//    suffix is numbered until it is unique (-copy-2, (Kopie 3) …); the user can
//    still rename in the form — the name field is editable in create mode.
import { t } from '../i18n'

// Envelope fields stamped by the resource store (storage.normalizeDoc).
const ENVELOPE = ['id', 'tenantId', 'version', 'createdAt', 'updatedAt'] as const

// Slug-like names keep a hyphenated suffix; anything with whitespace or
// characters outside a conservative identifier set gets the spoken suffix.
const SLUG = /^[A-Za-z0-9._:@/-]+$/

export function stripMeta<T extends object>(doc: T, extra: readonly string[] = []): T {
  const drop = new Set<string>([...ENVELOPE, ...extra])
  return Object.fromEntries(Object.entries(doc).filter(([k]) => !drop.has(k))) as T
}

export function copyName(name: string, existing: Iterable<string> = []): string {
  const taken = new Set(existing)
  const slug = SLUG.test(name)
  const suffix = t('copySuffix') // "(Kopie)" / "(copy)"
  const candidate = (n: number): string => {
    if (slug) return n === 1 ? `${name}-copy` : `${name}-copy-${n}`
    if (n === 1) return `${name} ${suffix}`
    // "(Kopie)" → "(Kopie 2)"; a suffix without a closing paren just gets the number appended.
    const numbered = suffix.endsWith(')') ? `${suffix.slice(0, -1)} ${n})` : `${suffix} ${n}`
    return `${name} ${numbered}`
  }
  for (let n = 1; ; n++) {
    const c = candidate(n)
    if (!taken.has(c)) return c
  }
}

// duplicateDoc: envelope-free copy of `doc` under a fresh, non-colliding name.
export function duplicateDoc<T extends { name: string }>(
  doc: T, existing: Iterable<string> = [], extraStrip: readonly string[] = [],
): T {
  return { ...stripMeta(doc, extraStrip), name: copyName(doc.name, existing) }
}
