package tts

import (
	"testing"

	"github.com/northplane/northplane/internal/model"
)

func mustNorm(t *testing.T, cfg model.TTSNormalize) *Normalizer {
	t.Helper()
	n, err := NewNormalizer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestNormalizeDefaultsEnglish(t *testing.T) {
	n := mustNorm(t, model.TTSNormalize{})
	cases := map[string]string{
		"Northplane alert. Severity CRITICAL. CPU load high on np-01. Press 4 to acknowledge, 6 to resolve.": "Northplane alert. Severity CRITICAL. C P U load high on N P 0 1. Press 4 to acknowledge, 6 to resolve.",
		"Disk /var/log 95% full on web01 (10.0.0.12)":                                                        "Disk slash var slash log 95 percent full on web 0 1, 10 dot 0 dot 0 dot 12.",
		"Latency 250ms > 200ms threshold; errors=12":                                                         "Latency 250 milliseconds greater than 200 milliseconds threshold, errors equals 12.",
		"Ticket #47110 opened":                         "Ticket number 4 7 1 1 0 opened.",
		"k8s pod OOMKilled in prod":                    "Kubernetes pod out of memory killed in production.",
		"See https://grafana.example.net/d/abc?x=1":    "See Grafana dot example dot net.",
		"VMs down: 3":                                  "V M s down: 3.",
		"Host db-master-02 unreachable":                "Host D B master 0 2 unreachable.",
		"Version v1.2.3 deployed":                      "Version v 1 dot 2 dot 3 deployed.",
		"5-10 errors/min":                              "5 to 10 errors per minute.",
		"backup_job failed":                            "backup job failed.",
		"Temp 21.5°C, humidity 45 %":                   "temperature 21.5 degrees Celsius, humidity 45 percent.",
		"Mail from ops@example.com bounced":            "Mail from ops at example dot com bounced.",
		"Node vm104 rebooted at 12:30":                 "Node V M 1 0 4 rebooted at 12:30.",
		"RAID degraded on nas01":                       "RAID degraded on nas 0 1.",
		"1 s, 2 s, 1.5 GB":                             "1 second, 2 seconds, 1.5 gigabytes.",
		"Call +49 171 1234567":                         "Call plus 49 171 1 2 3 4 5 6 7.",
		"Server 4711 is down":                          "Server 4711 is down.",
		"Job 2026-08-23T10:00:00Z failed":              "Job 2026-08-23, 10:00 failed.",
		"HTTP 503 from api-gw":                         "H T T P 503 from A P I gateway.",
		"**bold** and `code` <b>html</b> 🔥":            "bold and code H T M L.",
		"Conns 1200 -> 3400 (~25%)":                    "connections 1200 to 3400, about 25 percent.",
		"DISK CRITICAL - PING OK, enter PIN, SSH DOWN": "DISK CRITICAL, PING okay, enter PIN, S S H DOWN.",
		"FEHLER: API 503, RAID degraded":               "FEHLER: A P I 503, RAID degraded.",
		"":                                             "",
	}
	for in, want := range cases {
		if got := n.Apply(in, "en-US"); got != want {
			t.Errorf("%q:\n got %q\nwant %q", in, got, want)
		}
	}
}

func TestNormalizeGerman(t *testing.T) {
	n := mustNorm(t, model.TTSNormalize{})
	cases := map[string]string{
		"Northplane Alarm. Schweregrad KRITISCH. Festplatte /var voll auf srv-01. Drücken Sie 4 zum Quittieren.": "Northplane Alarm. Schweregrad KRITISCH. Festplatte Slash var voll auf Server 0 1. Drücken Sie 4 zum Quittieren.",
		"Temperatur 21.5°C im Serverraum":  "Temperatur 21,5 Grad Celsius im Serverraum.",
		"Speicher 90% belegt, 2 GB frei":   "Speicher 90 Prozent belegt, 2 Gigabyte frei.",
		"USV auf Batterie, ca. 12 min":     "U S V auf Batterie, circa 12 Minuten.",
		"Ping 10.1.2.3 fehlgeschlagen":     "Ping 10 Punkt 1 Punkt 2 Punkt 3 fehlgeschlagen.",
		"DB-Replikation hängt (Lag 3600s)": "D B Replikation hängt, Lag 3600 Sekunden.",
	}
	for in, want := range cases {
		if got := n.Apply(in, "de-DE"); got != want {
			t.Errorf("%q:\n got %q\nwant %q", in, got, want)
		}
	}
}

func TestNormalizeModes(t *testing.T) {
	words := mustNorm(t, model.TTSNormalize{Numbers: "words"})
	if got := words.Apply("Server 4711", "en"); got != "Server four thousand seven hundred eleven." {
		t.Errorf("words en: %q", got)
	}
	if got := words.Apply("Server 4711", "de"); got != "Server viertausendsiebenhundertelf." {
		t.Errorf("words de: %q", got)
	}
	if got := words.Apply("Server 4711", "fr"); got != "Server 4711." { // no French writer → auto
		t.Errorf("words fr: %q", got)
	}
	digits := mustNorm(t, model.TTSNormalize{Numbers: "digits"})
	if got := digits.Apply("Server 4711", "en"); got != "Server 4 7 1 1." {
		t.Errorf("digits: %q", got)
	}
	native := mustNorm(t, model.TTSNormalize{Numbers: "native", Symbols: "native", Units: "native",
		IPAddresses: "native", Identifiers: "keep", Acronyms: "off", URLs: "keep", NoBuiltinLexicon: true})
	in := "CPU 95% on web01 10.0.0.1 12345 https://x.io/a"
	if got := native.Apply(in, "en"); got != in+"." {
		t.Errorf("native: %q", got)
	}
	off := mustNorm(t, model.TTSNormalize{Disabled: true})
	if got := off.Apply("k8s 95% <b>x</b>", "en"); got != "k8s 95% x." {
		t.Errorf("disabled: %q", got)
	}
	from3 := mustNorm(t, model.TTSNormalize{DigitsFrom: 3})
	if got := from3.Apply("Room 101 and 12", "en"); got != "Room 1 0 1 and 12." {
		t.Errorf("digitsFrom: %q", got)
	}
}

func TestNormalizeLexiconRegexSpell(t *testing.T) {
	n := mustNorm(t, model.TTSNormalize{
		Lexicon: []model.TTSLexiconEntry{
			{From: "np-01", To: "Server eins"},
			{From: "SQL", To: "sequel", MatchCase: true},
			{From: "foo", To: "bar", Substring: true},
		},
		Regex:    []model.TTSRegexRule{{Pattern: `(?i)srv(\d+)`, Replace: "Server $1"}},
		SpellOut: []string{"acme"},
	})
	cases := map[string]string{
		"np-01 down, NP-01 down":  "Server eins down, Server eins down.",
		"SQL vs sql":              "sequel versus S Q L.",
		"foobar foofoo":           "barbar barbar.",
		"srv12 and SRV3":          "Server 12 and Server 3.",
		"ACME portal, acme-login": "A C M E portal, A C M E login.",
	}
	for in, want := range cases {
		if got := n.Apply(in, "en"); got != want {
			t.Errorf("%q:\n got %q\nwant %q", in, got, want)
		}
	}
	if _, err := NewNormalizer(model.TTSNormalize{Regex: []model.TTSRegexRule{{Pattern: "(", Replace: ""}}}); err == nil {
		t.Fatal("bad regex must be rejected")
	}
}

func TestNumberWords(t *testing.T) {
	cases := []struct {
		n    int64
		lang string
		want string
	}{
		{0, "en", "zero"}, {21, "en", "twenty-one"}, {100, "en", "one hundred"},
		{1001, "en", "one thousand one"}, {1234567, "en", "one million two hundred thirty-four thousand five hundred sixty-seven"},
		{-5, "en", "minus five"},
		{0, "de", "null"}, {1, "de", "eins"}, {21, "de", "einundzwanzig"}, {100, "de", "einhundert"},
		{101, "de", "einhunderteins"}, {1000, "de", "eintausend"}, {2024, "de", "zweitausendvierundzwanzig"},
		{1000000, "de", "eine Million"}, {2500000, "de", "zwei Millionen fünfhunderttausend"},
		{1000000000, "de", "eine Milliarde"},
	}
	for _, c := range cases {
		got, ok := NumberWords(c.n, c.lang)
		if !ok || got != c.want {
			t.Errorf("%d/%s: got %q (%v) want %q", c.n, c.lang, got, ok, c.want)
		}
	}
	if _, ok := NumberWords(5, "fr"); ok {
		t.Error("french should be unsupported")
	}
}
