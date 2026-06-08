// Maintenance: silences + downtimes management (SPEC §6.3/§9.2).
import { useState } from 'react'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { t } from '../i18n'
import { SilencesTab } from '../components/alerting/Silences'
import { DowntimesTab } from '../components/alerting/Downtimes'

const tabs = ['silences', 'downtimes'] as const
type Tab = typeof tabs[number]

export function MaintenancePage() {
  const [tab, setTab] = useState<Tab>('silences')
  return (
    <div className="space-y-4">
      <h1 className="text-lg font-bold">{t('maintenance')}</h1>
      <Tabs value={tab} onValueChange={(v) => setTab(v as Tab)}>
        <TabsList>
          {tabs.map((tb) => <TabsTrigger key={tb} value={tb}>{t(tb)}</TabsTrigger>)}
        </TabsList>
      </Tabs>
      {tab === 'silences' && <SilencesTab />}
      {tab === 'downtimes' && <DowntimesTab />}
    </div>
  )
}
