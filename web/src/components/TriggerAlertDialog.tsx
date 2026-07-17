// Manual alarm dialog (POST /api/v1/alerts): title/message/severity/
// escalation policy plus an optional collapsible "Alarm-App Sound" section
// whose values travel as np.* labels (np.sound / np.volume /
// np.overrideSilent) for the Northplane alarm app.
import { useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { ChevronDown, ChevronRight } from 'lucide-react'
import { post, queryClient, resourceApi } from '../api'
import type { Alert, EscalationPolicy } from '../types'
import { Dialog, DialogContent, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from '@/components/ui/select'
import { Field, FormError, SubmitRow } from '@/components/kit'
import { ToggleRow } from './alerting/common'
import { t } from '../i18n'

const policiesApi = resourceApi<EscalationPolicy>('escalation-policies')

// Radix <SelectItem> cannot use "" — sentinel for the "no selection" option.
const NONE = '__none__'

// Manual alarms are always problems — no 'ok' severity here.
const MANUAL_SEVERITIES = ['critical', 'warning', 'info'] as const
type ManualSeverity = (typeof MANUAL_SEVERITIES)[number]

// Alarm-app sounds (np.sound label contract, see northplane-alarm README).
const NP_SOUNDS = ['none', 'np_klaxon', 'np_sirene', 'np_puls'] as const
const NP_VOLUMES = ['0.1', '0.2', '0.3', '0.4', '0.5', '0.6', '0.7', '0.8', '0.9', '1.0'] as const

export function TriggerAlertDialog({ onClose, onRaised }: {
  onClose: () => void; onRaised?: () => void
}) {
  const [title, setTitle] = useState('')
  const [message, setMessage] = useState('')
  const [severity, setSeverity] = useState<ManualSeverity>('critical')
  const [policy, setPolicy] = useState<string>(NONE)
  const [soundOpen, setSoundOpen] = useState(false)
  const [sound, setSound] = useState<string>('none')
  const [volume, setVolume] = useState('') // '' = app default
  const [overrideSilent, setOverrideSilent] = useState(false)

  const { data: policies } = useQuery({ queryKey: policiesApi.queryKey, queryFn: policiesApi.list })

  const raise = useMutation({
    mutationFn: () => {
      const labels: Record<string, string> = {}
      if (sound !== 'none') labels['np.sound'] = sound
      if (volume) labels['np.volume'] = volume
      if (overrideSilent) labels['np.overrideSilent'] = 'true'
      return post<Alert>('/alerts', {
        title: title.trim(),
        message: message.trim() || undefined,
        severity,
        escalationPolicy: policy === NONE ? undefined : policy,
        labels: Object.keys(labels).length ? labels : undefined,
      })
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['alerts'] })
      onRaised?.()
      onClose()
    },
  })

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    if (!title.trim()) return
    raise.mutate()
  }

  return (
    <Dialog open onOpenChange={(o) => { if (!o) onClose() }}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>{t('triggerAlert')}</DialogTitle>
        </DialogHeader>
        <form onSubmit={onSubmit} className="space-y-3">
          <Field label={t('title')} required>
            <Input value={title} autoFocus required
              onChange={(e) => setTitle(e.target.value)}
              placeholder="Feueralarm Halle 2" />
          </Field>

          <Field label={t('message')} hint={t('optional')}>
            <Textarea value={message} rows={3}
              onChange={(e) => setMessage(e.target.value)} />
          </Field>

          <div className="grid grid-cols-2 gap-3">
            <Field label={t('severityLevel')}>
              <Select value={severity} onValueChange={(v) => setSeverity(v as ManualSeverity)}>
                <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
                <SelectContent>
                  {MANUAL_SEVERITIES.map((s) => <SelectItem key={s} value={s}>{s}</SelectItem>)}
                </SelectContent>
              </Select>
            </Field>
            <Field label={t('escalationPolicyField')}>
              <Select value={policy} onValueChange={setPolicy}>
                <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value={NONE}>— {t('none')} —</SelectItem>
                  {(policies ?? []).map((p) => <SelectItem key={p.name} value={p.name}>{p.name}</SelectItem>)}
                </SelectContent>
              </Select>
            </Field>
          </div>

          {/* Collapsible Alarm-App sound section → np.* labels */}
          <div className="border border-border rounded-lg">
            <button type="button"
              className="flex items-center gap-1.5 w-full px-3 py-2 text-xs font-semibold text-foreground/90 cursor-pointer"
              aria-expanded={soundOpen}
              onClick={() => setSoundOpen((v) => !v)}>
              {soundOpen ? <ChevronDown size={13} /> : <ChevronRight size={13} />}
              {t('alarmAppSound')}
            </button>
            {soundOpen && (
              <div className="px-3 pb-3 space-y-2">
                <p className="text-[11px] text-muted-foreground">{t('alarmAppSoundHint')}</p>
                <div className="grid grid-cols-2 gap-3">
                  <Field label={t('npSound')}>
                    <Select value={sound} onValueChange={setSound}>
                      <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
                      <SelectContent>
                        {NP_SOUNDS.map((s) => (
                          <SelectItem key={s} value={s}>{s === 'none' ? t('none') : s}</SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </Field>
                  <Field label={t('npVolume')}>
                    <Select value={volume || NONE} onValueChange={(v) => setVolume(v === NONE ? '' : v)}>
                      <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
                      <SelectContent>
                        <SelectItem value={NONE}>{t('defaultParen')}</SelectItem>
                        {NP_VOLUMES.map((v) => <SelectItem key={v} value={v}>{v}</SelectItem>)}
                      </SelectContent>
                    </Select>
                  </Field>
                </div>
                <ToggleRow label={t('npOverrideSilent')} checked={overrideSilent} onChange={setOverrideSilent} />
              </div>
            )}
          </div>

          <FormError error={raise.error} />
          <SubmitRow onCancel={onClose} saving={raise.isPending}
            disabled={!title.trim()} label={t('triggerAlert')} />
        </form>
      </DialogContent>
    </Dialog>
  )
}
