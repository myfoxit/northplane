// Agent enrollment snippets (pure data/functions, separated from the
// Agents tab component for react-refresh and unit tests).
import { t } from '../../i18n'

export const TOKEN_PLACEHOLDER = 'np_<TOKEN>'
export const INSTALL_CMD = 'curl -fsSL https://raw.githubusercontent.com/myfoxit/northplane/main/install.sh | sh'

// Comments follow the UI language (I18N-3) — the keys themselves are the
// agent's real config vocabulary and stay as-is.
export function agentYaml(server: string, token: string, hostname: string): string {
  return [
    `server: ${server}`,
    `token: ${token}`,
    hostname ? `hostname: ${hostname}` : `# hostname: web-01   # ${t('agentYamlHostnameDefault')}`,
    'interval: 60s',
    'disk: ["/"]',
    `# net: ["eth0"]   # ${t('agentYamlNetHint')}`,
    `# ${t('agentYamlActiveMode')}`,
    '# listen: ":5693"',
    `# listenToken: ${t('agentYamlListenToken')}`,
    `# ${t('agentYamlCustomChecks')}`,
    '# checks:',
    '#   - service: check_postgres',
    '#     command: check_postgres',
    '#     args: ["--host", "localhost"]',
    '#     timeout: 30s',
  ].join('\n')
}

export const SYSTEMD_UNIT = `[Unit]
Description=Northplane host agent
After=network-online.target

[Service]
ExecStart=/usr/local/bin/np-agent -config /etc/northplane/agent.yaml
Restart=always
RestartSec=5
DynamicUser=yes

[Install]
WantedBy=multi-user.target`

export const LAUNCHD_PLIST = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>com.northplane.agent</string>
  <key>ProgramArguments</key><array>
    <string>/usr/local/bin/np-agent</string>
    <string>-config</string><string>/etc/northplane/agent.yaml</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
</dict></plist>`

export const WINDOWS_SERVICE = `sc.exe create np-agent binPath= "C:\\Program Files\\northplane\\np-agent.exe -config C:\\ProgramData\\northplane\\agent.yaml" start= auto
sc.exe start np-agent`
