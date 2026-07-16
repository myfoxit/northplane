// Appearance (Admin → Darstellung): a colour-theme switcher. The app ships the
// Northplane dark palette; this tab lets a user swap the whole palette for the
// stock shadcn neutral theme or any tweakcn preset, via a swatch grid where
// each tile previews the theme's colours. The choice is a per-browser
// preference (localStorage) applied by toggling <html data-theme> — see
// theme.ts and the :root[data-theme] blocks in index.css.
import { Check } from 'lucide-react'
import { Card, CardHeader, CardTitle, CardDescription, CardContent } from '@/components/ui/card'
import { cn } from '@/lib/utils'
import { t } from '../../i18n'
import { THEMES, useTheme, setTheme } from '../../theme'

// A compact palette preview: the theme's representative colours as a strip.
function Swatch({ colors }: { colors: string[] }) {
  return (
    <span className="flex h-6 w-11 shrink-0 overflow-hidden rounded-md border border-border/60" aria-hidden>
      {colors.map((c, i) => (
        <span key={i} className="flex-1" style={{ backgroundColor: c }} />
      ))}
    </span>
  )
}

export function AppearanceTab() {
  const active = useTheme()
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('colorTheme')}</CardTitle>
        <CardDescription>{t('colorThemeHint')} · {THEMES.length}</CardDescription>
      </CardHeader>
      <CardContent>
        {/* Swatch grid — one selectable tile per theme. radiogroup semantics
            so the whole set reads as one exclusive choice (keyboard + AT). */}
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
                onClick={() => setTheme(theme.id)}
                className={cn(
                  'flex items-center gap-2.5 rounded-lg border px-2.5 py-2 text-left text-sm transition-colors cursor-pointer',
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
      </CardContent>
    </Card>
  )
}
