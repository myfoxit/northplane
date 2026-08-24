// Customer switcher for the CMP multi-customer console (SPEC §7.7: a central
// operator manages many fully-isolated customer tenants from one console).
//
// Visible only to operators whose effective permissions include admin:tenants
// (or "*"); everyone else sees nothing and stays pinned to their own tenant.
// Selecting a customer scopes the ENTIRE UI to that tenant via the
// X-Northplane-Tenant header (tenant.ts → api.ts), then resets the query cache
// and navigates home so no other customer's data lingers.
//
// Polaris: the trigger is a workspace-style card — avatar tile (stable
// per-tenant hue) + name + role line — instead of a bare form select. A
// non-home selection tints the trigger in the rail accent and keeps the
// standing "active customer" line; Layout.tsx additionally shows a full-width
// scope banner, so the operator can never mistake which customer they are
// operating on.
import { useQuery } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { get, queryClient, type ListResponse } from '../api'
import { setActiveTenantId, useActiveTenant } from '../tenant'
import { hasPermission } from '../permissions'
import type { Tenant, Whoami } from '../types'
import { t } from '../i18n'
import {
  Select, SelectContent, SelectGroup, SelectItem, SelectLabel, SelectTrigger,
} from './ui/select'

// Radix Select forbids an empty-string item value, so "operator's own tenant"
// gets a sentinel that maps back to null (no header).
const HOME = '__home__'

// Stable avatar identity: initials + a hue hashed from the tenant name, so a
// customer keeps its tile color across sessions and lists.
function initialsOf(name: string): string {
  // Words by whitespace first; a single CamelCase word splits on its humps
  // ("MyFoxIT" → My/Fox/IT → MF); otherwise the first two characters.
  let parts = name.trim().split(/\s+/).filter(Boolean)
  if (parts.length < 2) {
    const humps = name.match(/[A-ZÄÖÜ][a-zäöüß]+|[A-ZÄÖÜ]+(?![a-zäöüß])|\d+/g)
    if (humps && humps.length >= 2) parts = humps
  }
  const chars = parts.length >= 2 ? `${parts[0]?.[0] ?? ''}${parts[1]?.[0] ?? ''}` : name.slice(0, 2)
  return chars.toUpperCase() || '·'
}
function hueOf(name: string): number {
  let h = 0
  for (const ch of name) h = (h * 31 + ch.codePointAt(0)!) % 360
  return h
}
function AvatarTile({ name, size = 24 }: { name: string; size?: number }) {
  return (
    <span
      aria-hidden
      className="inline-flex items-center justify-center shrink-0 rounded-md font-semibold text-white/90"
      style={{
        width: size, height: size, fontSize: Math.round(size * 0.4),
        background: `hsl(${hueOf(name)} 38% 34%)`,
      }}
    >
      {initialsOf(name)}
    </span>
  )
}

export function TenantSwitcher() {
  const navigate = useNavigate()
  const active = useActiveTenant()

  const { data: me } = useQuery({
    queryKey: ['whoami'],
    queryFn: () => get<Whoami>('/whoami'),
    staleTime: 5 * 60_000,
  })
  // Wildcard-aware (see permissions.ts): the built-in admin role holds "*:*",
  // which a literal string compare would miss — hiding the switcher from the
  // very operator who is allowed to use it.
  const canSwitch = hasPermission(me?.permissions, 'admin:tenants')

  const { data: tenants } = useQuery({
    queryKey: ['tenants'],
    queryFn: () => get<ListResponse<Tenant>>('/tenants').then((r) => r.items ?? []),
    enabled: canSwitch,
    staleTime: 5 * 60_000,
  })

  // Nothing to show for non-cross-tenant operators.
  if (!canSwitch) return null

  const onChange = (v: string) => {
    setActiveTenantId(v === HOME ? null : v)
    // A different customer ⇒ every cached list/detail is now wrong. Drop the
    // whole cache and return home so nothing renders the previous customer.
    queryClient.clear()
    void navigate({ to: '/' })
  }

  const activeName = active
    ? (tenants?.find((c) => c.id === active)?.name ?? active)
    : null
  // In the "your tenant" (home) view, surface which tenant that actually is so
  // the operator can see their scope at a glance (NP-16).
  const homeName = me?.tenantId ? (tenants?.find((c) => c.id === me.tenantId)?.name ?? null) : null
  const triggerName = activeName ?? homeName ?? t('yourTenant')
  // The operator's own tenant is the HOME row — listing it again under the
  // customers would offer scoping the console onto itself.
  const customers = (tenants ?? []).filter((c) => c.id !== me?.tenantId)

  return (
    <div className="px-3 pb-2 border-b border-sidebar-border">
      <Select value={active ?? HOME} onValueChange={onChange}>
        <SelectTrigger
          aria-label={t('customer')}
          className={`w-full h-auto py-1.5 px-2 rounded-[10px] border bg-sidebar-accent/70 ${
            active
              ? 'border-sidebar-primary/60 bg-sidebar-primary/10'
              : 'border-sidebar-border hover:border-sidebar-ring/50'}`}
        >
          <span className="flex items-center gap-2 min-w-0 text-left">
            <AvatarTile name={triggerName} size={26} />
            <span className="min-w-0 flex flex-col leading-tight">
              <span className="truncate text-[12.5px] font-semibold text-sidebar-accent-foreground">{triggerName}</span>
              <span className={`text-[10.5px] ${active ? 'text-sidebar-primary' : 'text-sidebar-foreground/60'}`}>
                {active ? t('customer') : t('yourTenant')}
              </span>
            </span>
          </span>
        </SelectTrigger>
        <SelectContent position="popper" align="start" sideOffset={6} className="w-(--radix-select-trigger-width)">
          <SelectGroup>
            <SelectLabel className="text-[10px] font-semibold uppercase tracking-[0.08em]">{t('yourTenant')}</SelectLabel>
            <SelectItem value={HOME} leading={<AvatarTile name={homeName ?? t('yourTenant')} size={20} />}>
              {homeName ?? t('yourTenant')}
            </SelectItem>
          </SelectGroup>
          {customers.length > 0 && (
            <SelectGroup>
              <SelectLabel className="text-[10px] font-semibold uppercase tracking-[0.08em]">{t('customers')}</SelectLabel>
              {customers.map((c) => (
                <SelectItem key={c.id} value={c.id} leading={<AvatarTile name={c.name} size={20} />}>
                  {c.name}
                </SelectItem>
              ))}
            </SelectGroup>
          )}
        </SelectContent>
      </Select>
      {active && (
        <div className="mt-1.5 rounded-md bg-sidebar-primary/12 border border-sidebar-primary/35 px-2 py-1 text-[10px] font-medium uppercase tracking-wide text-sidebar-primary">
          {t('activeCustomer')}: {activeName}
        </div>
      )}
    </div>
  )
}
