// Alerting configuration: rules (CEL) + groups + escalation policies
// (CMP Alarmserver Webmin parity, SPEC §9.2/§9.4).
import { useRef, useState } from 'react'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Button } from '@/components/ui/button'
import { t } from '../i18n'
import { RulesTab } from '../components/alerting/Rules'
import { GroupsTab } from '../components/alerting/Groups'
import { EscalationsTab } from '../components/alerting/Escalations'
import { IVRMenusTab } from '../components/alerting/IVRMenus'

const tabs = ['rules', 'groups', 'escalations', 'ivr'] as const
type Tab = typeof tabs[number]

const labels: Record<Tab, string> = {
  rules: t('rules'),
  groups: t('groups'),
  escalations: t('escalations'),
  ivr: t('ivrMenus'),
}

export function AlertingConfigPage() {
  const [tab, setTab] = useState<Tab>('rules')
  // The active tab registers its "open create dialog" handler here so the
  // primary Create button can live on the header/tab row instead of floating
  // mid-page over the empty state (NP-13).
  const createRef = useRef<() => void>(() => {})
  const createLabel = tab === 'rules' ? t('newRule') : t('create')
  return (
    <div className="space-y-4">
      {/* Heading tracks the active tab (NP-06) instead of always "Alert rules". */}
      <div className="flex items-center justify-between gap-2">
        <h1 className="text-lg font-bold">{labels[tab]}</h1>
        <Button variant="default" onClick={() => createRef.current()}>{createLabel}</Button>
      </div>
      <Tabs value={tab} onValueChange={(v) => setTab(v as Tab)}>
        <TabsList>
          {tabs.map((tb) => <TabsTrigger key={tb} value={tb}>{labels[tb]}</TabsTrigger>)}
        </TabsList>
      </Tabs>
      {tab === 'rules' && <RulesTab createRef={createRef} />}
      {tab === 'groups' && <GroupsTab createRef={createRef} />}
      {tab === 'escalations' && <EscalationsTab createRef={createRef} />}
      {tab === 'ivr' && <IVRMenusTab createRef={createRef} />}
    </div>
  )
}
