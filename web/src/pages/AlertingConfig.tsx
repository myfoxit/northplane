// Alerting configuration: rules (CEL) + groups + escalation policies
// (CMP Alarmserver Webmin parity, SPEC §9.2/§9.4).
import { useState } from 'react'
import { TabBar } from '../components/ui'
import { t } from '../i18n'
import { RulesTab } from '../components/alerting/Rules'
import { GroupsTab } from '../components/alerting/Groups'
import { EscalationsTab } from '../components/alerting/Escalations'

const tabs = ['rules', 'groups', 'escalations'] as const
type Tab = typeof tabs[number]

const labels: Record<Tab, string> = {
  rules: t('rules'),
  groups: 'Gruppen',
  escalations: t('escalations'),
}

export function AlertingConfigPage() {
  const [tab, setTab] = useState<Tab>('rules')
  return (
    <div className="space-y-4">
      <h1 className="text-lg font-bold">{t('rules')}</h1>
      <TabBar tabs={tabs} value={tab} onChange={setTab} labels={(tb) => labels[tb]} />
      {tab === 'rules' && <RulesTab />}
      {tab === 'groups' && <GroupsTab />}
      {tab === 'escalations' && <EscalationsTab />}
    </div>
  )
}
