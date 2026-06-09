// Agent enrollment snippets (pure data/functions, separated from the
// Agents tab component for react-refresh and unit tests).

export const TOKEN_PLACEHOLDER = 'np_<TOKEN>'
export const INSTALL_CMD = 'curl -fsSL https://raw.githubusercontent.com/myfoxit/northplane/main/install.sh | sh'

export function agentYaml(server: string, token: string, hostname: string): string {
  return [
    `server: ${server}`,
    `token: ${token}`,
    hostname ? `hostname: ${hostname}` : '# hostname: web-01   # Standard: OS-Hostname',
    'interval: 60s',
    'disk: ["/"]',
    '# net: ["eth0"]   # optional: Interfaces filtern (Standard: alle)',
    '# Aktiver Modus (Server fragt den Agent ab, NCPA-Stil) — optional:',
    '# listen: ":5693"',
    '# listenToken: <eigenes-langes-secret>',
    '# Eigene Nagios-Plugin-Checks — optional:',
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
