// Maintenance: silences + downtimes management (SPEC §6.3/§9.2).
import { useRef, useState } from 'react'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Button } from '@/components/ui/button'
import { t } from '../i18n'
import { SilencesTab } from '../components/alerting/Silences'
import { DowntimesTab } from '../components/alerting/Downtimes'

const tabs = ['silences', 'downtimes'] as const
type Tab = typeof tabs[number]

export function MaintenancePage() {
  const [tab, setTab] = useState<Tab>('silences')
  // Active tab registers its create handler so Create sits on the header/tab
  // row rather than floating over the empty state (NP-13).
  const createRef = useRef<() => void>(() => {})
  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between gap-2">
        <h1 className="text-lg font-bold">{t('maintenance')}</h1>
        <Button variant="default" onClick={() => createRef.current()}>{t('create')}</Button>
      </div>
      <Tabs value={tab} onValueChange={(v) => setTab(v as Tab)}>
        <TabsList>
          {tabs.map((tb) => <TabsTrigger key={tb} value={tb}>{t(tb)}</TabsTrigger>)}
        </TabsList>
      </Tabs>
      {tab === 'silences' && <SilencesTab createRef={createRef} />}
      {tab === 'downtimes' && <DowntimesTab createRef={createRef} />}
    </div>
  )
}
