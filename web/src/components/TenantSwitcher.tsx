// Customer switcher for the CMP multi-customer console (SPEC §7.7: a central
// operator manages many fully-isolated customer tenants from one console).
//
// Visible only to operators whose effective permissions include admin:tenants
// (or "*"); everyone else sees nothing and stays pinned to their own tenant.
// Selecting a customer scopes the ENTIRE UI to that tenant via the
// X-Northplane-Tenant header (tenant.ts → api.ts), then resets the query cache
// and navigates home so no other customer's data lingers. A non-home selection
// is rendered with a warning treatment so the operator can never mistake which
// customer they are operating on.
import { useQuery } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { Building2 } from 'lucide-react'
import { get, queryClient, type ListResponse } from '../api'
import { setActiveTenantId, useActiveTenant } from '../tenant'
import { hasPermission } from '../permissions'
import type { Tenant, Whoami } from '../types'
import { t } from '../i18n'
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from './ui/select'

// Radix Select forbids an empty-string item value, so "operator's own tenant"
// gets a sentinel that maps back to null (no header).
const HOME = '__home__'

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

  return (
    <div className="px-3 py-2 border-b border-border/80">
      <Select value={active ?? HOME} onValueChange={onChange}>
        <SelectTrigger
          size="sm"
          aria-label={t('customer')}
          className={`w-full ${active ? 'border-amber-500/70 bg-amber-500/10 text-amber-600 dark:text-amber-400' : ''}`}
        >
          <Building2 size={13} className="shrink-0" />
          <SelectValue placeholder={t('customer')} />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={HOME}>{t('yourTenant')}</SelectItem>
          {(tenants ?? []).map((c) => (
            <SelectItem key={c.id} value={c.id}>{c.name}</SelectItem>
          ))}
        </SelectContent>
      </Select>
      {active && (
        <div className="mt-1 text-[10px] font-medium uppercase tracking-wide text-amber-600 dark:text-amber-400">
          {t('activeCustomer')}: {activeName}
        </div>
      )}
    </div>
  )
}
