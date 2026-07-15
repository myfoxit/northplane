// Config bundles (SPEC §11.6): export the whole configuration as YAML,
// and plan→review→apply a pasted bundle — the GitOps round-trip that
// previously had API endpoints but no UI. Apply uses the two-phase token
// from :plan so exactly the reviewed diff is applied.
import { useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { api } from '../../api'
import { Button } from '@/components/ui/button'
import { Card, CardHeader, CardTitle, CardDescription, CardContent } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Table, TableHeader, TableBody, TableRow, TableHead, TableCell } from '@/components/ui/table'
import { Textarea } from '@/components/ui/textarea'
import { FormError } from '@/components/kit'
import { t } from '../../i18n'

interface PlanAction {
  action: 'create' | 'update' | 'delete'
  kind: string
  name: string
  host?: string
  diff?: Record<string, unknown>
}
interface PlanResult {
  plan: PlanAction[]
  warnings?: string[]
  applyToken?: string
}

const ACTION_BADGE: Record<PlanAction['action'], string> = {
  create: 'bg-emerald-500/10 text-emerald-400 border-emerald-800',
  update: 'bg-amber-500/10 text-amber-400 border-amber-800',
  delete: 'bg-red-500/10 text-red-400 border-red-800',
}

export function BundlesTab() {
  const [yaml, setYaml] = useState('')
  const [plan, setPlan] = useState<PlanResult | null>(null)
  const [applied, setApplied] = useState('')

  const doPlan = useMutation({
    mutationFn: () => api<PlanResult>('/config/bundles:plan', {
      method: 'POST', body: yaml, headers: { 'Content-Type': 'application/yaml' },
    }),
    onSuccess: (r) => {
      setPlan(r)
      setApplied('')
    },
  })
  const doApply = useMutation({
    mutationFn: () => api<PlanResult>(`/config/bundles:apply?applyToken=${encodeURIComponent(plan?.applyToken ?? '')}`, {
      method: 'POST',
    }),
    onSuccess: (r) => {
      setApplied(`✓ ${t('applied')} (${r.plan?.length ?? 0} ${t('changes')})`)
      setPlan(null)
    },
  })

  return (
    <div className="space-y-4">
      <Card>
        <CardHeader>
          <CardTitle>Export</CardTitle>
          <CardDescription>{t('bundleExportDesc')}</CardDescription>
        </CardHeader>
        <CardContent>
          <Button asChild variant="outline">
            <a href="/api/v1/config/bundles:export" download="northplane-bundle.yaml">{t('downloadBundleYaml')}</a>
          </Button>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Plan &amp; Apply</CardTitle>
          <CardDescription>{t('bundlePlanDesc')}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-3">
          <Textarea
            value={yaml}
            onChange={(e) => { setYaml(e.target.value); setPlan(null) }}
            placeholder={'kind: Host\nmetadata:\n  name: web-01\nspec:\n  address: 10.0.0.10\n---\n…'}
            className="font-mono text-xs min-h-40"
          />
          <div className="flex gap-2 items-center">
            <Button onClick={() => doPlan.mutate()} disabled={!yaml.trim() || doPlan.isPending}>{t('planDryRun')}</Button>
            {plan?.applyToken && (
              <Button variant="destructive" onClick={() => doApply.mutate()} disabled={doApply.isPending}>
                {t('apply')} ({plan.plan.length} {t('changes')})
              </Button>
            )}
            {plan && plan.plan.length === 0 && <span className="text-sm text-muted-foreground">{t('noChangesIdentical')}</span>}
            {applied && <span className="text-sm text-emerald-400">{applied}</span>}
          </div>
          <FormError error={doPlan.error ?? doApply.error} />
          {(plan?.warnings?.length ?? 0) > 0 && (
            <div className="text-xs text-amber-400 space-y-1">
              {plan!.warnings!.map((w, i) => <div key={i}>⚠ {w}</div>)}
            </div>
          )}
          {plan && plan.plan.length > 0 && (
            <Table>
              <TableHeader>
                <TableRow>
                  {[t('action'), 'Kind', 'Name', 'Diff'].map((h) => <TableHead key={h}>{h}</TableHead>)}
                </TableRow>
              </TableHeader>
              <TableBody>
                {plan.plan.map((a, i) => (
                  <TableRow key={i}>
                    <TableCell><Badge variant="outline" className={ACTION_BADGE[a.action]}>{a.action}</Badge></TableCell>
                    <TableCell className="text-xs">{a.kind}</TableCell>
                    <TableCell className="text-xs font-mono">{a.host ? `${a.host}/` : ''}{a.name}</TableCell>
                    <TableCell className="text-xs font-mono text-muted-foreground truncate max-w-md">
                      {a.diff ? JSON.stringify(a.diff) : '—'}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </div>
  )
}
