// Appearance (Admin → Darstellung): two theming axes — a light/dark MODE and a
// colour THEME. Every theme comes in both modes (its natural Ondoki design +
// a derived counterpart), so the user can pick a theme and flip the mode to
// see it both ways. Mode toggles <html class="light"> (mode.ts), theme toggles
// <html data-theme> (theme.ts); see the :root / :root.light blocks in
// index.css.
//
// Both axes are the INSTANCE's branding, not a personal preference: a change
// here re-skins the console for everyone signing into this installation
// (branding.ts persists it). That is why the controls are gated on
// config:write — the API enforces the same rule, this only avoids offering a
// button that would 403. Read-only viewers still see which theme is active.
import { Check, Sun, Moon, Monitor } from 'lucide-react'
import type { ComponentType } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Card, CardHeader, CardTitle, CardDescription, CardContent } from '@/components/ui/card'
import { cn } from '@/lib/utils'
import { get } from '../../api'
import { hasPermission } from '../../permissions'
import type { Whoami } from '../../types'
import { t } from '../../i18n'
import { THEMES, useTheme, setTheme } from '../../theme'
import { MODES, useMode, setMode, type Mode } from '../../mode'

const MODE_META: Record<Mode, { labelKey: 'modeSystem' | 'modeLight' | 'modeDark'; icon: ComponentType<{ size?: number }> }> = {
  system: { labelKey: 'modeSystem', icon: Monitor },
  light: { labelKey: 'modeLight', icon: Sun },
  dark: { labelKey: 'modeDark', icon: Moon },
}

// A compact palette preview: the theme's representative colours as a strip.
function Swatch({ colors }: { colors: string[] }) {
  return (
    <span className="flex h-6 w-11 shrink-0 overflow-hidden rounded-md border border-border/60" aria-hidden>
      {colors.map((c, i) => <span key={i} className="flex-1" style={{ backgroundColor: c }} />)}
    </span>
  )
}

function ModeToggle({ disabled }: { disabled: boolean }) {
  const active = useMode()
  return (
    <div role="radiogroup" aria-label={t('mode')} className="inline-flex rounded-lg border border-border bg-card p-0.5">
      {MODES.map((m) => {
        const { labelKey, icon: Icon } = MODE_META[m]
        const selected = m === active
        return (
          <button
            key={m}
            type="button"
            role="radio"
            aria-checked={selected}
            disabled={disabled}
            onClick={() => setMode(m)}
            className={cn(
              'flex items-center gap-1.5 rounded-md px-3 py-1.5 text-xs font-medium transition-colors',
              disabled ? 'cursor-not-allowed opacity-60' : 'cursor-pointer',
              selected ? 'bg-primary/15 text-primary' : 'text-muted-foreground hover:text-foreground',
            )}
          >
            <Icon size={14} /> {t(labelKey)}
          </button>
        )
      })}
    </div>
  )
}

export function AppearanceTab() {
  const active = useTheme()
  const { data: me } = useQuery({
    queryKey: ['whoami'],
    queryFn: () => get<Whoami>('/whoami'),
    staleTime: 5 * 60_000,
  })
  // Wildcard-aware (permissions.ts): the built-in admin role holds "*:*".
  // Undefined while whoami is in flight ⇒ locked, so the controls never flash
  // enabled for someone who cannot use them.
  const canEdit = hasPermission(me?.permissions, 'config:write')
  const locked = !canEdit
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('appearance')}</CardTitle>
        <CardDescription>{t('colorThemeHint')}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-5">
        {locked && (
          <div className="rounded-lg border border-border bg-muted/40 px-3 py-2 text-xs text-muted-foreground">
            {t('brandingReadOnly')}
          </div>
        )}
        {/* MODE axis */}
        <div className="space-y-2">
          <div className="text-xs font-medium text-muted-foreground">{t('mode')} · {t('modeHint')}</div>
          <ModeToggle disabled={locked} />
        </div>

        {/* THEME axis */}
        <div className="space-y-2">
          <div className="text-xs font-medium text-muted-foreground">{t('colorTheme')} · {THEMES.length}</div>
          {/* radiogroup semantics so the set reads as one exclusive choice. */}
          <div role="radiogroup" aria-label={t('colorTheme')}
            className="grid grid-cols-2 gap-2 sm:grid-cols-3 lg:grid-cols-4">
            {THEMES.map((theme) => {
              const selected = theme.id === active
              return (
                <button
                  key={theme.id}
                  type="button"
                  role="radio"
                  aria-checked={selected}
                  title={theme.label}
                  disabled={locked}
                  onClick={() => setTheme(theme.id)}
                  className={cn(
                    'flex items-center gap-2.5 rounded-lg border px-2.5 py-2 text-left text-sm transition-colors',
                    locked ? 'cursor-not-allowed opacity-60' : 'cursor-pointer',
                    selected
                      ? 'border-primary bg-primary/10 text-foreground'
                      : 'border-border bg-card text-muted-foreground hover:border-input hover:text-foreground',
                  )}
                >
                  <Swatch colors={theme.swatch} />
                  <span className="min-w-0 flex-1 truncate">{theme.label}</span>
                  {selected && <Check size={15} className="shrink-0 text-primary" />}
                </button>
              )
            })}
          </div>
        </div>
      </CardContent>
    </Card>
  )
}
