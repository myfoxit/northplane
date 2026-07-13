// Permission matching for the UI.
//
// This is a faithful port of the backend authority check
// (`model.Permission.Implies`, internal/model/admin.go): a permission the
// operator *holds* implies a permission we *want* when it is the super-admin
// grant ("*" or "*:*"), an exact match, or a resource/action wildcard
// ("admin:*", "*:read"). The UI MUST use this rather than comparing strings
// literally — the built-in admin role is granted "*:*", which a literal
// `p === 'admin:tenants'` / `p === '*'` check silently misses, hiding
// admin-only UI (e.g. the customer switcher) from the actual super-admin.

// permImplies reports whether a held permission grants `want`.
export function permImplies(have: string, want: string): boolean {
  if (have === want || have === '*:*' || have === '*') return true
  const hi = have.indexOf(':')
  const wi = want.indexOf(':')
  if (hi < 0 || wi < 0) return false // malformed / unqualified perm implies nothing
  const hr = have.slice(0, hi)
  const ha = have.slice(hi + 1)
  const wr = want.slice(0, wi)
  const wa = want.slice(wi + 1)
  return (hr === '*' || hr === wr) && (ha === '*' || ha === wa)
}

// hasPermission reports whether any of the operator's effective permissions
// grants `want`. Mirrors backend Principal.Allow.
export function hasPermission(
  perms: readonly string[] | null | undefined,
  want: string,
): boolean {
  return !!perms?.some((p) => permImplies(p, want))
}
