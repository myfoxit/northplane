// App shell (Polaris): brand rail with grouped nav, top bar (search ⌘K,
// refresh cadence, assistant ⌘I, docs, logout, account), customer-scope
// banner, command palette and AI sidebar. Keyboard-first per SPEC §12.4.
import { type ReactNode, type ComponentType, useEffect, useState } from 'react'
import { Link, useRouterState, useNavigate } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import {
  LayoutDashboard, TriangleAlert, Server, Bell, Siren, Activity, LayoutGrid,
  Network, FileText, Zap, Phone, Wrench, Files, Telescope, Settings, Sparkles, Search, LogOut,
  Bot, BookOpen, Building2,
} from 'lucide-react'
import { t } from '../i18n'
import { get, queryClient } from '../api'
import type { Overview as OverviewData, Whoami } from '../types'
import { setActiveTenantId, useActiveTenant } from '../tenant'
import { hasPermission } from '../permissions'
import type { Tenant } from '../types'
import type { ListResponse } from '../api'
import { syncPreferencesFromServer } from '../settings'
import { syncBrandingFromServer } from '../branding'
import { CommandPalette } from './CommandPalette'
import { AISidebar } from './AISidebar'
import { RefreshControl } from './RefreshControl'
import { TenantSwitcher } from './TenantSwitcher'

type IconType = ComponentType<{ size?: number; className?: string }>

// NorthMark — the one Northplane logo mark (Polaris star on an accent tile).
// Replaces the old triangle-vs-radar split; login/setup render the same star
// server-side (internal/web).
export function NorthMark({ size = 28, radius = 8 }: { size?: number; radius?: number }) {
  return (
    <span
      aria-hidden
      className="inline-flex items-center justify-center shrink-0"
      style={{
        width: size, height: size, borderRadius: radius,
        backgroundImage: 'linear-gradient(140deg, color-mix(in oklab, var(--sidebar-primary, var(--primary)) 90%, white 10%), color-mix(in oklab, var(--sidebar-primary, var(--primary)) 70%, black 30%))',
      }}
    >
      <svg width={Math.round(size * 0.54)} height={Math.round(size * 0.54)} viewBox="0 0 24 24" fill="none">
        <path d="M12 2.6 L14.1 9.9 L21.4 12 L14.1 14.1 L12 21.4 L9.9 14.1 L2.6 12 L9.9 9.9 Z" fill="#FFFFFF" />
      </svg>
    </span>
  )
}

interface NavItem { to: string; label: string; icon: IconType; count?: number }

export function Layout({ children }: { children: ReactNode }) {
  const [paletteOpen, setPaletteOpen] = useState(false)
  const [aiOpen, setAIOpen] = useState(false)
  const router = useRouterState()
  const navigate = useNavigate()
  const active = useActiveTenant()

  // Adopt server-side state once per shell mount; both keep a localStorage
  // cache for instant boot. Preferences are per user; branding is the
  // instance's look and is deliberately NOT re-fetched on a tenant switch —
  // the console must not re-skin when an operator changes customer.
  useEffect(() => {
    void syncPreferencesFromServer()
    void syncBrandingFromServer()
  }, [])

  // Shared caches (same keys as Overview / TenantSwitcher — one fetch, many
  // readers): summary counts for the nav badges, identity for the avatar,
  // tenant names for the scope banner.
  const { data: ov } = useQuery({
    queryKey: ['overview'],
    queryFn: () => get<OverviewData>('/overview'),
    refetchInterval: 60_000,
    staleTime: 30_000,
  })
  const { data: me } = useQuery({
    queryKey: ['whoami'],
    queryFn: () => get<Whoami>('/whoami'),
    staleTime: 5 * 60_000,
  })
  const canSwitch = hasPermission(me?.permissions, 'admin:tenants')
  const { data: tenants } = useQuery({
    queryKey: ['tenants'],
    queryFn: () => get<ListResponse<Tenant>>('/tenants').then((r) => r.items ?? []),
    enabled: canSwitch,
    staleTime: 5 * 60_000,
  })
  const activeName = active ? (tenants?.find((c) => c.id === active)?.name ?? active) : null

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key === 'k') {
        e.preventDefault()
        setPaletteOpen((v) => !v)
      }
      if ((e.metaKey || e.ctrlKey) && e.key === 'i') {
        e.preventDefault()
        setAIOpen((v) => !v)
      }
      // g-shortcuts when not typing
      if (e.key === 'g' && !isTyping()) {
        const next = (e2: KeyboardEvent) => {
          const map: Record<string, string> = {
            o: '/', p: '/problems', h: '/objects', a: '/alerts', e: '/events',
          }
          if (map[e2.key]) navigate({ to: map[e2.key] })
          window.removeEventListener('keydown', next)
        }
        window.addEventListener('keydown', next, { once: true })
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [navigate])

  const isWallboard = 'wallboard' in (router.location.search as Record<string, unknown>)
  if (isWallboard) {
    return <div className="min-h-screen bg-background text-foreground">{children}</div>
  }

  const s = ov?.summary
  const problemCount = s
    ? (s.hostsDown ?? 0) + (s.hostsUnreachable ?? 0) + (s.servicesCritical ?? 0) + (s.servicesWarning ?? 0) + (s.servicesUnknown ?? 0)
    : 0
  const alertCount = (ov?.openAlerts?.critical ?? 0) + (ov?.openAlerts?.warning ?? 0)

  const groups: { label: string; items: NavItem[] }[] = [
    {
      label: t('navMonitor'),
      items: [
        { to: '/', label: t('overview'), icon: LayoutDashboard },
        { to: '/problems', label: t('problems'), icon: TriangleAlert, count: problemCount },
        { to: '/objects', label: t('objects'), icon: Server },
        { to: '/events', label: t('events'), icon: Activity },
      ],
    },
    {
      label: t('navRespond'),
      items: [
        { to: '/alerts', label: t('alerts'), icon: Bell, count: alertCount },
        { to: '/incidents', label: t('incidents'), icon: Siren },
        { to: '/oncall', label: t('oncall'), icon: Phone },
        { to: '/maintenance', label: t('maintenance'), icon: Wrench },
      ],
    },
    {
      label: t('navAnalyze'),
      items: [
        { to: '/dashboards', label: t('dashboards'), icon: LayoutGrid },
        { to: '/business', label: t('business'), icon: Network },
        { to: '/reports', label: t('reports'), icon: FileText },
      ],
    },
    {
      label: t('navConfigure'),
      items: [
        { to: '/alerting', label: t('rules'), icon: Zap },
        { to: '/templates', label: t('templates'), icon: Files },
        { to: '/discovery', label: t('discovery'), icon: Telescope },
        { to: '/agent', label: t('agent'), icon: Bot },
      ],
    },
  ]

  const navLink = (item: NavItem) => {
    const isActive = item.to === '/'
      ? router.location.pathname === '/'
      : router.location.pathname.startsWith(item.to)
    const Icon = item.icon
    return (
      <Link
        key={item.to} to={item.to}
        className={`flex items-center gap-2.5 h-[30px] px-2.5 mx-2 rounded-lg text-[13px] transition-colors ${
          isActive ? 'bg-sidebar-primary/15 text-sidebar-accent-foreground font-medium'
            : 'text-sidebar-foreground/75 hover:text-sidebar-foreground hover:bg-sidebar-accent'}`}
      >
        <Icon size={16} className={`shrink-0 ${isActive ? 'text-sidebar-primary' : ''}`} />
        <span className="flex-1 truncate">{item.label}</span>
        {(item.count ?? 0) > 0 && (
          <span aria-hidden className="text-[10.5px] font-semibold tabular-nums text-danger bg-danger/15 rounded-full px-1.5 py-px">
            {item.count}
          </span>
        )}
      </Link>
    )
  }

  // Initials for the account chip ("Administrator" → AD, "Max Muster" → MM).
  const initials = (name?: string) => {
    if (!name) return '·'
    const parts = name.trim().split(/\s+/)
    const chars = parts.length >= 2 ? `${parts[0]?.[0] ?? ''}${parts[1]?.[0] ?? ''}` : name.slice(0, 2)
    return chars.toUpperCase()
  }

  const exitCustomer = () => {
    setActiveTenantId(null)
    queryClient.clear()
    void navigate({ to: '/' })
  }

  return (
    <div className="min-h-screen bg-background text-foreground flex">
      {/* The shell rail reads the --sidebar* tokens so a colour theme can give
          it its own palette (incl. a dark sidebar over a light body). */}
      <aside className="w-60 shrink-0 bg-sidebar text-sidebar-foreground border-r border-sidebar-border flex flex-col">
        <div className="px-4 pt-4 pb-3 flex items-center gap-2.5">
          <NorthMark size={28} />
          <span className="font-display font-semibold text-[15.5px] tracking-tight text-sidebar-accent-foreground">Northplane</span>
        </div>
        <TenantSwitcher />
        <nav className="flex-1 py-2 overflow-y-auto">
          {groups.map((g) => (
            <div key={g.label} className="mb-1.5">
              <div className="px-4 pt-2.5 pb-1 text-[10px] font-semibold uppercase tracking-[0.09em] text-sidebar-foreground/45">{g.label}</div>
              {g.items.map(navLink)}
            </div>
          ))}
        </nav>
        <div className="border-t border-sidebar-border py-2.5">
          {navLink({ to: '/admin', label: t('admin'), icon: Settings })}
        </div>
      </aside>

      <div className="flex-1 min-w-0 flex flex-col">
        {/* Top bar: global utilities live here, not in the rail. */}
        <header className="h-[52px] shrink-0 border-b border-border flex items-center gap-2 px-5">
          <button
            onClick={() => setPaletteOpen(true)}
            className="w-80 max-w-[40vw] h-8 flex items-center gap-2 rounded-lg border border-input bg-secondary/50 px-2.5 text-xs text-muted-foreground hover:border-ring/60 hover:text-foreground cursor-pointer transition-colors"
          >
            <Search size={13} className="shrink-0" /> {t('search')}
          </button>
          <div className="flex-1" />
          <RefreshControl />
          <div className="w-px h-5 bg-border mx-1" aria-hidden />
          <button
            onClick={() => setAIOpen((v) => !v)}
            className="h-8 flex items-center gap-1.5 rounded-lg border border-input px-2.5 text-xs font-medium text-foreground/85 hover:border-primary hover:text-foreground cursor-pointer transition-colors"
          >
            <Sparkles size={13} className="text-primary" /> {t('assistant')} (⌘I)
          </button>
          <div className="w-px h-5 bg-border mx-1" aria-hidden />
          {/* The product manual ships inside the binary (internal/docs) and is
              served publicly at /docs/ — open in a new tab so the operator
              keeps their place in the app. */}
          <a
            href="/docs/" target="_blank" rel="noopener" aria-label={t('documentation')} title={t('documentation')}
            className="size-8 flex items-center justify-center rounded-lg text-muted-foreground hover:text-foreground hover:bg-accent transition-colors"
          >
            <BookOpen size={15} />
          </a>
          <a
            href="/auth/logout" aria-label="Logout" title="Logout"
            className="size-8 flex items-center justify-center rounded-lg text-muted-foreground hover:text-foreground hover:bg-accent transition-colors"
          >
            <LogOut size={15} />
          </a>
          <span
            title={me?.name}
            className="ml-1 size-7 rounded-full bg-primary/15 text-primary text-[10.5px] font-semibold flex items-center justify-center select-none"
          >
            {initials(me?.name)}
          </span>
        </header>

        {/* Customer-scope banner: when the console is operating a customer
            tenant, the scope is unmissable and one click returns home. */}
        {active && (
          <div className="h-9 shrink-0 flex items-center gap-2.5 px-5 bg-primary/12 border-b border-primary/40 text-[12.5px]">
            <Building2 size={13} className="text-primary shrink-0" />
            <span className="truncate text-foreground/90">
              {t('activeCustomer')}: <span className="font-semibold text-foreground">{activeName}</span>
            </span>
            <div className="flex-1" />
            <button onClick={exitCustomer} className="font-medium text-primary hover:underline cursor-pointer shrink-0">
              {t('exitToYourTenant')}
            </button>
          </div>
        )}

        {/* pb-24: the floating assistant bubble sits bottom-right — the
            extra scroll room keeps it from covering the last table row and
            its row actions (WIDGET-1). */}
        <main className="flex-1 min-w-0 p-5 pb-24 overflow-auto">{children}</main>
      </div>

      {aiOpen && <AISidebar onClose={() => setAIOpen(false)} />}
      <CommandPalette open={paletteOpen} onClose={() => setPaletteOpen(false)} />
    </div>
  )
}

function isTyping(): boolean {
  const el = document.activeElement
  return !!el && (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA' || (el as HTMLElement).isContentEditable)
}
