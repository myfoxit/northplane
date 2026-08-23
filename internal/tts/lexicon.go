package tts

import "strings"

// langWords are the spoken forms of symbols and units per language.
// English is the fallback for every language without its own table —
// an engine reading "dot" inside a French sentence is still far better
// than a swallowed IP address.
type langWords struct {
	dot, slash, at, percent, and, plus, equals, arrow string
	less, greater, number, about, degrees, minus      string
	euro, dollar, pound, to, not, per                 string
	// units: abbreviation (lower-case) → singular|plural spoken form
	units map[string][2]string
}

var wordsEN = langWords{
	dot: "dot", slash: "slash", at: "at", percent: "percent", and: "and", plus: "plus",
	equals: "equals", arrow: "to", less: "less than", greater: "greater than",
	number: "number", about: "about", degrees: "degrees", minus: "minus",
	euro: "euros", dollar: "dollars", pound: "pounds", to: "to", not: "not", per: "per",
	units: map[string][2]string{
		"ns": {"nanosecond", "nanoseconds"}, "us": {"microsecond", "microseconds"}, "µs": {"microsecond", "microseconds"},
		"ms": {"millisecond", "milliseconds"}, "s": {"second", "seconds"}, "sec": {"second", "seconds"},
		"min": {"minute", "minutes"}, "m": {"minute", "minutes"}, "h": {"hour", "hours"}, "hrs": {"hours", "hours"}, "d": {"day", "days"},
		"b": {"byte", "bytes"}, "kb": {"kilobyte", "kilobytes"}, "mb": {"megabyte", "megabytes"},
		"gb": {"gigabyte", "gigabytes"}, "tb": {"terabyte", "terabytes"}, "pb": {"petabyte", "petabytes"},
		"kib": {"kibibyte", "kibibytes"}, "mib": {"mebibyte", "mebibytes"}, "gib": {"gibibyte", "gibibytes"}, "tib": {"tebibyte", "tebibytes"},
		"bit": {"bit", "bits"}, "kbit": {"kilobit", "kilobits"}, "mbit": {"megabit", "megabits"}, "gbit": {"gigabit", "gigabits"},
		"kbps": {"kilobit per second", "kilobits per second"}, "mbps": {"megabit per second", "megabits per second"},
		"gbps": {"gigabit per second", "gigabits per second"}, "kbit/s": {"kilobit per second", "kilobits per second"},
		"mbit/s": {"megabit per second", "megabits per second"}, "gbit/s": {"gigabit per second", "gigabits per second"},
		"kb/s": {"kilobyte per second", "kilobytes per second"}, "mb/s": {"megabyte per second", "megabytes per second"},
		"gb/s": {"gigabyte per second", "gigabytes per second"}, "b/s": {"byte per second", "bytes per second"},
		"hz": {"hertz", "hertz"}, "khz": {"kilohertz", "kilohertz"}, "mhz": {"megahertz", "megahertz"}, "ghz": {"gigahertz", "gigahertz"},
		"°c": {"degrees Celsius", "degrees Celsius"}, "°f": {"degrees Fahrenheit", "degrees Fahrenheit"},
		"v": {"volt", "volts"}, "a": {"ampere", "amperes"}, "w": {"watt", "watts"}, "kw": {"kilowatt", "kilowatts"},
		"mw": {"megawatt", "megawatts"}, "kwh": {"kilowatt hour", "kilowatt hours"}, "dbm": {"d B m", "d B m"}, "db": {"decibel", "decibels"},
		"rpm": {"R P M", "R P M"}, "ppm": {"P P M", "P P M"}, "iops": {"I O P S", "I O P S"},
		"req/s": {"request per second", "requests per second"}, "r/s": {"request per second", "requests per second"},
		"ops/s": {"operation per second", "operations per second"}, "pkt/s": {"packet per second", "packets per second"},
		"km": {"kilometer", "kilometers"}, "cm": {"centimeter", "centimeters"}, "mm": {"millimeter", "millimeters"},
		"kg": {"kilogram", "kilograms"}, "g": {"gram", "grams"}, "l": {"liter", "liters"}, "ml": {"milliliter", "milliliters"},
		"bar": {"bar", "bar"}, "mbar": {"millibar", "millibar"}, "hpa": {"hectopascal", "hectopascal"}, "pa": {"pascal", "pascal"},
		"lux": {"lux", "lux"}, "lx": {"lux", "lux"},
	},
}

var wordsDE = langWords{
	dot: "Punkt", slash: "Slash", at: "at", percent: "Prozent", and: "und", plus: "plus",
	equals: "gleich", arrow: "nach", less: "kleiner als", greater: "größer als",
	number: "Nummer", about: "circa", degrees: "Grad", minus: "minus",
	euro: "Euro", dollar: "Dollar", pound: "Pfund", to: "bis", not: "nicht", per: "pro",
	units: map[string][2]string{
		"ns": {"Nanosekunde", "Nanosekunden"}, "us": {"Mikrosekunde", "Mikrosekunden"}, "µs": {"Mikrosekunde", "Mikrosekunden"},
		"ms": {"Millisekunde", "Millisekunden"}, "s": {"Sekunde", "Sekunden"}, "sec": {"Sekunde", "Sekunden"}, "sek": {"Sekunde", "Sekunden"},
		"min": {"Minute", "Minuten"}, "m": {"Minute", "Minuten"}, "h": {"Stunde", "Stunden"}, "std": {"Stunde", "Stunden"}, "d": {"Tag", "Tage"},
		"b": {"Byte", "Byte"}, "kb": {"Kilobyte", "Kilobyte"}, "mb": {"Megabyte", "Megabyte"},
		"gb": {"Gigabyte", "Gigabyte"}, "tb": {"Terabyte", "Terabyte"}, "pb": {"Petabyte", "Petabyte"},
		"kib": {"Kibibyte", "Kibibyte"}, "mib": {"Mebibyte", "Mebibyte"}, "gib": {"Gibibyte", "Gibibyte"}, "tib": {"Tebibyte", "Tebibyte"},
		"bit": {"Bit", "Bit"}, "kbit": {"Kilobit", "Kilobit"}, "mbit": {"Megabit", "Megabit"}, "gbit": {"Gigabit", "Gigabit"},
		"kbps": {"Kilobit pro Sekunde", "Kilobit pro Sekunde"}, "mbps": {"Megabit pro Sekunde", "Megabit pro Sekunde"},
		"gbps": {"Gigabit pro Sekunde", "Gigabit pro Sekunde"}, "kbit/s": {"Kilobit pro Sekunde", "Kilobit pro Sekunde"},
		"mbit/s": {"Megabit pro Sekunde", "Megabit pro Sekunde"}, "gbit/s": {"Gigabit pro Sekunde", "Gigabit pro Sekunde"},
		"kb/s": {"Kilobyte pro Sekunde", "Kilobyte pro Sekunde"}, "mb/s": {"Megabyte pro Sekunde", "Megabyte pro Sekunde"},
		"gb/s": {"Gigabyte pro Sekunde", "Gigabyte pro Sekunde"}, "b/s": {"Byte pro Sekunde", "Byte pro Sekunde"},
		"hz": {"Hertz", "Hertz"}, "khz": {"Kilohertz", "Kilohertz"}, "mhz": {"Megahertz", "Megahertz"}, "ghz": {"Gigahertz", "Gigahertz"},
		"°c": {"Grad Celsius", "Grad Celsius"}, "°f": {"Grad Fahrenheit", "Grad Fahrenheit"},
		"v": {"Volt", "Volt"}, "a": {"Ampere", "Ampere"}, "w": {"Watt", "Watt"}, "kw": {"Kilowatt", "Kilowatt"},
		"mw": {"Megawatt", "Megawatt"}, "kwh": {"Kilowattstunde", "Kilowattstunden"}, "dbm": {"d B m", "d B m"}, "db": {"Dezibel", "Dezibel"},
		"rpm": {"Umdrehungen pro Minute", "Umdrehungen pro Minute"}, "ppm": {"P P M", "P P M"}, "iops": {"I O P S", "I O P S"},
		"req/s": {"Anfrage pro Sekunde", "Anfragen pro Sekunde"}, "r/s": {"Anfrage pro Sekunde", "Anfragen pro Sekunde"},
		"ops/s": {"Operation pro Sekunde", "Operationen pro Sekunde"}, "pkt/s": {"Paket pro Sekunde", "Pakete pro Sekunde"},
		"km": {"Kilometer", "Kilometer"}, "cm": {"Zentimeter", "Zentimeter"}, "mm": {"Millimeter", "Millimeter"},
		"kg": {"Kilogramm", "Kilogramm"}, "g": {"Gramm", "Gramm"}, "l": {"Liter", "Liter"}, "ml": {"Milliliter", "Milliliter"},
		"bar": {"Bar", "Bar"}, "mbar": {"Millibar", "Millibar"}, "hpa": {"Hektopascal", "Hektopascal"}, "pa": {"Pascal", "Pascal"},
		"lux": {"Lux", "Lux"}, "lx": {"Lux", "Lux"},
	},
}

// Smaller tables for further languages: symbols only, units fall back to
// English (engines read "10 megabytes" acceptably in any language; the
// symbol words matter far more for intelligibility).
var wordsFR = langWords{dot: "point", slash: "slash", at: "arobase", percent: "pour cent", and: "et", plus: "plus",
	equals: "égale", arrow: "vers", less: "inférieur à", greater: "supérieur à", number: "numéro", about: "environ",
	degrees: "degrés", minus: "moins", euro: "euros", dollar: "dollars", pound: "livres", to: "à", not: "pas", per: "par", units: wordsEN.units}
var wordsES = langWords{dot: "punto", slash: "barra", at: "arroba", percent: "por ciento", and: "y", plus: "más",
	equals: "igual a", arrow: "a", less: "menor que", greater: "mayor que", number: "número", about: "aproximadamente",
	degrees: "grados", minus: "menos", euro: "euros", dollar: "dólares", pound: "libras", to: "a", not: "no", per: "por", units: wordsEN.units}
var wordsIT = langWords{dot: "punto", slash: "barra", at: "chiocciola", percent: "per cento", and: "e", plus: "più",
	equals: "uguale a", arrow: "a", less: "minore di", greater: "maggiore di", number: "numero", about: "circa",
	degrees: "gradi", minus: "meno", euro: "euro", dollar: "dollari", pound: "sterline", to: "a", not: "non", per: "al", units: wordsEN.units}
var wordsNL = langWords{dot: "punt", slash: "slash", at: "apenstaartje", percent: "procent", and: "en", plus: "plus",
	equals: "is gelijk aan", arrow: "naar", less: "kleiner dan", greater: "groter dan", number: "nummer", about: "ongeveer",
	degrees: "graden", minus: "min", euro: "euro", dollar: "dollar", pound: "pond", to: "tot", not: "niet", per: "per", units: wordsEN.units}
var wordsPT = langWords{dot: "ponto", slash: "barra", at: "arroba", percent: "por cento", and: "e", plus: "mais",
	equals: "igual a", arrow: "para", less: "menor que", greater: "maior que", number: "número", about: "cerca de",
	degrees: "graus", minus: "menos", euro: "euros", dollar: "dólares", pound: "libras", to: "a", not: "não", per: "por", units: wordsEN.units}
var wordsPL = langWords{dot: "kropka", slash: "ukośnik", at: "małpa", percent: "procent", and: "i", plus: "plus",
	equals: "równa się", arrow: "do", less: "mniej niż", greater: "więcej niż", number: "numer", about: "około",
	degrees: "stopni", minus: "minus", euro: "euro", dollar: "dolarów", pound: "funtów", to: "do", not: "nie", per: "na", units: wordsEN.units}

func wordsFor(lang string) langWords {
	switch langPrefix(lang) {
	case "de":
		return wordsDE
	case "fr":
		return wordsFR
	case "es":
		return wordsES
	case "it":
		return wordsIT
	case "nl":
		return wordsNL
	case "pt":
		return wordsPT
	case "pl":
		return wordsPL
	}
	return wordsEN
}

// --- built-in IT-operations lexicon ----------------------------------------

// builtinLexicon holds literal, case-insensitive whole-word replacements
// that make monitoring shorthand intelligible. Language "" applies to
// every language; keys are lower-case. Operators can disable it per
// profile (noBuiltinLexicon) or override single entries with their own
// lexicon (which runs first).
var builtinLexicon = map[string]map[string]string{
	"": {
		"k8s": "Kubernetes", "i/o": "I O", "io": "I O", "r/w": "read write",
		"cpu": "CPU", "cpus": "CPUs", "gpu": "GPU", "vm": "VM", "vms": "VMs", "vcpu": "V CPU",
		"db": "DB", "dbs": "DBs", "lb": "LB", "fw": "FW", "gw": "gateway", "vpn": "VPN", "wlan": "W LAN",
		"http": "HTTP", "https": "HTTPS", "ssh": "SSH", "ssl": "SSL", "tls": "TLS", "ftp": "FTP", "sftp": "SFTP",
		"smtp": "SMTP", "imap": "IMAP", "pop3": "POP 3", "dns": "DNS", "dhcp": "DHCP", "ntp": "NTP",
		"snmp": "SNMP", "tcp": "TCP", "udp": "UDP", "icmp": "ICMP", "bgp": "BGP", "ospf": "OSPF",
		"mqtt": "MQTT", "api": "API", "url": "URL", "uri": "URI", "ip": "IP", "ipv4": "IP v 4", "ipv6": "IP v 6",
		"sql": "SQL", "mysql": "My SQL", "postgresql": "Postgres", "pgsql": "Postgres", "mssql": "MS SQL",
		"nfs": "NFS", "smb": "SMB", "cifs": "CIFS", "iscsi": "i SCSI", "zfs": "ZFS", "lvm": "LVM",
		"ups": "UPS", "usv": "USV", "pdu": "PDU", "kvm": "KVM", "ipmi": "IPMI", "bmc": "BMC", "idrac": "i DRAC", "ilo": "i L O",
		"pbx": "PBX", "sip": "SIP", "pstn": "PSTN", "isdn": "ISDN", "voip": "Voice over IP",
		"ad": "AD", "ldap": "LDAP", "sso": "SSO", "mfa": "MFA", "2fa": "two factor",
		"ci/cd": "C I C D", "cicd": "C I C D", "k3s": "K 3 s", "aws": "AWS", "gcp": "GCP", "s3": "S 3", "ec2": "EC 2", "rds": "RDS",
		"os": "OS", "ui": "UI", "gui": "GUI", "cli": "CLI", "id": "ID", "ids": "IDs", "uuid": "UUID", "pid": "PID",
		"tx": "TX", "rx": "RX", "rtt": "RTT", "p95": "P 95", "p99": "P 99", "p50": "P 50",
		"oom": "out of memory", "oomkill": "out of memory kill", "oomkilled": "out of memory killed",
		"segfault": "segmentation fault", "cert": "certificate", "certs": "certificates",
		"ack": "acknowledge", "acked": "acknowledged", "nack": "not acknowledged",
		"crit": "critical", "warn": "warning", "unkn": "unknown", "unk": "unknown", "ok": "okay",
		"e.g.": "for example", "i.e.": "that is", "etc.": "et cetera", "etc": "et cetera", "vs": "versus", "vs.": "versus",
		"w/": "with", "w/o": "without", "approx.": "approximately", "incl.": "including", "excl.": "excluding",
		"min.": "minimum", "max.": "maximum", "avg": "average", "avg.": "average", "std.": "standard", "err": "error", "errs": "errors",
		"conn": "connection", "conns": "connections", "req": "request", "reqs": "requests", "resp": "response",
		"auth": "authentication", "authn": "authentication", "authz": "authorization", "cfg": "config", "conf": "config",
		"msg": "message", "msgs": "messages", "pkg": "package", "pkgs": "packages", "proc": "process", "procs": "processes",
		"mem": "memory", "util": "utilization", "utilisation": "utilization", "temp": "temperature", "tmp": "temp",
		"nr": "number", "nr.": "number", "no.": "number", "num": "number", "qty": "quantity", "iface": "interface", "ifaces": "interfaces",
		"env": "environment", "envs": "environments", "prod": "production", "stg": "staging", "dev": "development",
		"svc": "service", "svcs": "services", "srv": "server", "srvs": "servers", "sys": "system", "fs": "file system",
		"dir": "directory", "dirs": "directories", "bkp": "backup", "repl": "replication", "cron": "cron",
		"nginx": "engine X", "haproxy": "H A proxy", "pfsense": "P F sense", "opnsense": "O P N sense", "proxmox": "Proxmox",
		"grafana": "Grafana", "prometheus": "Prometheus", "zabbix": "Zabbix", "icinga": "Icinga", "nagios": "Nagios",
		"northplane": "Northplane", "np-agent": "N P agent",
	},
	"de": {
		"krit": "kritisch", "crit": "kritisch", "warn": "Warnung", "unkn": "unbekannt", "unk": "unbekannt",
		"e.g.": "zum Beispiel", "z.b.": "zum Beispiel", "d.h.": "das heißt", "bzw.": "beziehungsweise", "bzw": "beziehungsweise",
		"ca.": "circa", "usw.": "und so weiter", "inkl.": "inklusive", "exkl.": "exklusive", "ggf.": "gegebenenfalls",
		"evtl.": "eventuell", "u.a.": "unter anderem", "vgl.": "vergleiche", "std.": "Stunden", "min.": "Minuten", "sek.": "Sekunden",
		"etc.": "et cetera", "vs": "versus", "vs.": "versus", "w/": "mit", "w/o": "ohne", "approx.": "ungefähr", "incl.": "inklusive",
		"max.": "maximal", "avg": "Durchschnitt", "avg.": "Durchschnitt", "err": "Fehler", "errs": "Fehler",
		"conn": "Verbindung", "conns": "Verbindungen", "req": "Anfrage", "reqs": "Anfragen", "resp": "Antwort",
		"auth": "Authentifizierung", "cfg": "Konfiguration", "conf": "Konfiguration", "msg": "Nachricht", "msgs": "Nachrichten", //nolint:misspell // German
		"pkg": "Paket", "pkgs": "Pakete", "proc": "Prozess", "procs": "Prozesse", "mem": "Speicher", "util": "Auslastung",
		"temp": "Temperatur", "nr": "Nummer", "nr.": "Nummer", "num": "Nummer", "anz.": "Anzahl", "iface": "Interface",
		"env": "Umgebung", "prod": "Produktion", "stg": "Staging", "dev": "Entwicklung", "svc": "Service", "svcs": "Services", //nolint:misspell // German
		"srv": "Server", "srvs": "Server", "sys": "System", "fs": "Dateisystem", "dir": "Verzeichnis", "dirs": "Verzeichnisse",
		"bkp": "Backup", "repl": "Replikation", "gw": "Gateway", "oom": "Speichermangel", "oomkilled": "wegen Speichermangel beendet",
		"cert": "Zertifikat", "certs": "Zertifikate", "ack": "quittieren", "acked": "quittiert",
		"segfault": "Speicherzugriffsfehler", "voip": "Voice over IP", "usv": "USV", "ups": "USV",
		"i.e.": "das heißt", "etc": "et cetera", "excl.": "exklusive", "qty": "Anzahl", "tmp": "temporär",
	},
}

// spelledAcronyms are ALL-CAPS tokens that contain a vowel but are still
// spelled letter by letter in IT operations (CPU, API, URL, …). Tokens
// without a vowel (DHCP, SNMP, SSH) are always spelled; every other
// ALL-CAPS token (DISK, PING, ERROR, FEHLER, RAID, JSON …) is left
// untouched for the engine, which reads shouted words as words.
var spelledAcronyms = map[string]bool{
	"CPU": true, "GPU": true, "API": true, "URL": true, "URI": true, "UPS": true, "USV": true, "PDU": true,
	"IPMI": true, "BMC": true, "SIP": true, "OSPF": true, "EIGRP": true, "ICMP": true, "IGMP": true, "ARP": true,
	"LACP": true, "EVPN": true, "ACL": true, "NAC": true, "IDS": true, "IPS": true, "UTM": true, "CVE": true,
	"IOC": true, "APT": true, "IOT": true, "IP": true, "ID": true, "UUID": true, "AWS": true, "ISP": true,
	"SLA": true, "SLO": true, "SLI": true, "OLA": true, "KPI": true, "ERP": true, "CRM": true, "PLC": true,
	"OPC": true, "HDMI": true, "USB": true, "LED": true, "AD": true, "SSO": true, "MFA": true, "AP": true,
	"OT": true, "IT": true, "UI": true, "CLI": true, "OS": true, "QA": true, "UAT": true, "EOL": true,
	"EOF": true, "ETA": true, "FAQ": true, "PKI": true, "CA": true, "IAM": true, "OEM": true, "SD": true,
	"PC": true, "AC": true, "DC": true, "SOA": true, "EDI": true, "ETL": true, "EKS": true, "AKS": true,
	"GKE": true, "VPC": true, "EU": true, "USA": true, "UK": true, "AI": true, "IE": true, "PSU": true,
	"MIB": true, "OID": true, "SFP": true, "QSFP": true, "RTU": true, "IED": true, "HMI": true, "DCS": true,
	"MES": true, "OEE": true, "ECU": true, "ABS": true, "EOD": true, "FYI": true, "AKA": true, "IDE": true,
	"ECC": true, "AFP": true, "LTE": true, "UMTS": true, "RFID": true, "QR": true, "OIDC": true, "RPO": true,
	"RTO": true, "HA": true, "OCSP": true, "OTP": true, "UPN": true, "SID": true, "PID": true, "TID": true,
	"EC": true, "IAAS": true, "PAAS": true, "SAAS": true, "ESXI": true, "NVME": true, "PCIE": true, "POE": true,
	"ISO": true, "UTC": true, "CET": true, "CEST": true, "GMT": true, "AM": true, "PM": true, "ASN": true,
	"PTR": true, "MX": true, "TTL": true, "TCP": true, "UDP": true, "DOS": true, "DDOS": true,
	"EDR": true, "XDR": true, "CMDB": true, "ITSM": true, "ITIL": true,
}

// hasVowel reports whether a (lower-case) token contains a vowel — tokens
// without one ("dhcp", "srv", "pbx") cannot be pronounced and are spelled.
func hasVowel(lower string) bool {
	return strings.ContainsAny(lower, "aeiouyäöüàáâãåæèéêëìíîïòóôõøùúûœ")
}
