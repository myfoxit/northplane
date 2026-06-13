// App shell: sidebar nav, command palette (⌘K), AI sidebar toggle.
// Keyboard-first per SPEC §12.4.
import { type ReactNode, type ComponentType, useEffect, useState } from 'react'
import { Link, useRouterState, useNavigate } from '@tanstack/react-router'
import {
  Radar, LayoutDashboard, TriangleAlert, Server, Bell, Siren, Activity, LayoutGrid,
  Network, FileText, Zap, Phone, Wrench, Files, Telescope, Settings, Sparkles, Search, LogOut,
} from 'lucide-react'
import { t } from '../i18n'
import { syncPreferencesFromServer } from '../settings'
import { CommandPalette } from './CommandPalette'
import { AISidebar } from './AISidebar'
import { RefreshControl } from './RefreshControl'
import { TenantSwitcher } from './TenantSwitcher'

type IconType = ComponentType<{ size?: number; className?: string }>

const nav: { to: string; label: string; icon: IconType }[] = [
  { to: '/', label: t('overview'), icon: LayoutDashboard },
  { to: '/problems', label: t('problems'), icon: TriangleAlert },
  { to: '/objects', label: t('objects'), icon: Server },
  { to: '/alerts', label: t('alerts'), icon: Bell },
  { to: '/incidents', label: t('incidents'), icon: Siren },
  { to: '/events', label: t('events'), icon: Activity },
  { to: '/dashboards', label: t('dashboards'), icon: LayoutGrid },
  { to: '/business', label: t('business'), icon: Network },
  { to: '/reports', label: t('reports'), icon: FileText },
  { to: '/alerting', label: t('rules'), icon: Zap },
  { to: '/oncall', label: t('oncall'), icon: Phone },
  { to: '/maintenance', label: t('maintenance'), icon: Wrench },
  { to: '/templates', label: t('templates'), icon: Files },
  { to: '/discovery', label: t('discovery'), icon: Telescope },
  { to: '/admin', label: t('admin'), icon: Settings },
]

export function Layout({ children }: { children: ReactNode }) {
  const [paletteOpen, setPaletteOpen] = useState(false)
  const [aiOpen, setAIOpen] = useState(false)
  const router = useRouterState()
  const navigate = useNavigate()

  // Adopt server-side preferences once per shell mount (settings.ts keeps
  // the localStorage cache for instant boot).
  useEffect(() => { void syncPreferencesFromServer() }, [])

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

  return (
    <div className="min-h-screen bg-background text-foreground flex">
      <aside className="w-52 shrink-0 border-r border-border/80 flex flex-col">
        <div className="px-4 py-4 flex items-center gap-2 border-b border-border/80">
          <Radar className="text-primary" size={20} />
          <span className="font-bold tracking-tight">Northplane</span>
        </div>
        <TenantSwitcher />
        <nav className="flex-1 py-3">
          {nav.map((item) => {
            const active = item.to === '/'
              ? router.location.pathname === '/'
              : router.location.pathname.startsWith(item.to)
            const Icon = item.icon
            return (
              <Link
                key={item.to} to={item.to}
                className={`flex items-center gap-2.5 px-4 py-2 text-sm transition-colors ${
                  active ? 'text-primary bg-primary/10 border-r-2 border-primary'
                    : 'text-muted-foreground hover:text-foreground hover:bg-card'}`}
              >
                <Icon size={16} className="shrink-0" />
                {item.label}
              </Link>
            )
          })}
        </nav>
        <div className="p-3 border-t border-border/80 space-y-2">
          <RefreshControl />
          <button
            onClick={() => setPaletteOpen(true)}
            className="w-full flex items-center gap-2 text-xs text-muted-foreground bg-card border border-border rounded-lg px-3 py-1.5 hover:border-input cursor-pointer transition-colors"
          >
            <Search size={13} /> {t('search')}
          </button>
          <button
            onClick={() => setAIOpen((v) => !v)}
            className="w-full flex items-center gap-2 text-xs text-muted-foreground bg-card border border-border rounded-lg px-3 py-1.5 hover:border-primary cursor-pointer transition-colors"
          >
            <Sparkles size={13} /> {t('assistant')} (⌘I)
          </button>
          <a href="/auth/logout" className="flex items-center gap-1.5 text-xs text-muted-foreground hover:text-foreground px-1">
            <LogOut size={12} /> Logout
          </a>
        </div>
      </aside>
      <main className="flex-1 min-w-0 p-5 overflow-auto">{children}</main>
      {aiOpen && <AISidebar onClose={() => setAIOpen(false)} />}
      <CommandPalette open={paletteOpen} onClose={() => setPaletteOpen(false)} />
    </div>
  )
}

function isTyping(): boolean {
  const el = document.activeElement
  return !!el && (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA' || (el as HTMLElement).isContentEditable)
}
