// App shell: sidebar nav, command palette (⌘K), AI sidebar toggle.
// Keyboard-first per SPEC §12.4.
import { type ReactNode, useEffect, useState } from 'react'
import { Link, useRouterState, useNavigate } from '@tanstack/react-router'
import { t } from '../i18n'
import { useLiveUpdates } from '../api'
import { CommandPalette } from './CommandPalette'
import { AISidebar } from './AISidebar'

const nav = [
  { to: '/', label: t('overview'), icon: '▦' },
  { to: '/problems', label: t('problems'), icon: '▲' },
  { to: '/objects', label: t('objects'), icon: '☰' },
  { to: '/alerts', label: t('alerts'), icon: '◉' },
  { to: '/incidents', label: t('incidents'), icon: '☄' },
  { to: '/events', label: t('events'), icon: '≋' },
  { to: '/dashboards', label: t('dashboards'), icon: '◫' },
  { to: '/business', label: t('business'), icon: '⬡' },
  { to: '/reports', label: t('reports'), icon: '▤' },
  { to: '/alerting', label: t('rules'), icon: '⚡' },
  { to: '/oncall', label: t('oncall'), icon: '☎' },
  { to: '/maintenance', label: t('maintenance'), icon: '⏸' },
  { to: '/templates', label: t('templates'), icon: '⧉' },
  { to: '/discovery', label: t('discovery'), icon: '◎' },
  { to: '/admin', label: t('admin'), icon: '⚙' },
]

export function Layout({ children }: { children: ReactNode }) {
  const [paletteOpen, setPaletteOpen] = useState(false)
  const [aiOpen, setAIOpen] = useState(false)
  const router = useRouterState()
  const navigate = useNavigate()
  useLiveUpdates()

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
    return <div className="min-h-screen bg-slate-950 text-slate-200">{children}</div>
  }

  return (
    <div className="min-h-screen bg-slate-950 text-slate-200 flex">
      <aside className="w-52 shrink-0 border-r border-slate-800/80 flex flex-col">
        <div className="px-4 py-4 flex items-center gap-2 border-b border-slate-800/80">
          <span className="text-blue-400 text-lg">▲</span>
          <span className="font-bold tracking-tight">Northplane</span>
        </div>
        <nav className="flex-1 py-3">
          {nav.map((item) => {
            const active = item.to === '/'
              ? router.location.pathname === '/'
              : router.location.pathname.startsWith(item.to)
            return (
              <Link
                key={item.to} to={item.to}
                className={`flex items-center gap-2.5 px-4 py-2 text-sm transition-colors ${
                  active ? 'text-blue-400 bg-blue-500/10 border-r-2 border-blue-400'
                    : 'text-slate-400 hover:text-slate-200 hover:bg-slate-900'}`}
              >
                <span className="w-4 text-center">{item.icon}</span>
                {item.label}
              </Link>
            )
          })}
        </nav>
        <div className="p-3 border-t border-slate-800/80 space-y-2">
          <button
            onClick={() => setPaletteOpen(true)}
            className="w-full text-left text-xs text-slate-500 bg-slate-900 border border-slate-800 rounded-lg px-3 py-1.5 hover:border-slate-600 cursor-pointer"
          >
            {t('search')}
          </button>
          <button
            onClick={() => setAIOpen((v) => !v)}
            className="w-full text-left text-xs text-slate-400 bg-slate-900 border border-slate-800 rounded-lg px-3 py-1.5 hover:border-blue-600 cursor-pointer"
          >
            ✦ {t('assistant')} (⌘I)
          </button>
          <a href="/auth/logout" className="block text-xs text-slate-600 hover:text-slate-400 px-1">Logout</a>
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
