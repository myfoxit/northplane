package tts

import (
	"testing"
)

func TestDetectShortAlarmTexts(t *testing.T) {
	d := &Detector{Candidates: []string{"de-DE", "en-US"}, Default: "de-DE"}
	cases := map[string]string{
		"CPU load high on web01":     "en-US",
		"Festplatte voll auf srv-01": "de-DE",
		"Northplane alert. Severity CRITICAL. Disk /var full. Press 4 to acknowledge, 6 to resolve.":        "en-US",
		"Northplane Alarm. Schweregrad KRITISCH. Datenbank nicht erreichbar. Drücken Sie 4 zum Quittieren.": "de-DE",
		"The database server is unreachable since 10 minutes":                                               "en-US",
		"Der Datenbankserver ist seit 10 Minuten nicht erreichbar":                                          "de-DE",
		"USV auf Batterie":   "de-DE",
		"UPS on battery":     "en-US",
		"Service down":       "en-US",
		"Dienst ausgefallen": "de-DE",
		"":                   "de-DE", // undecided → default
		"4711":               "de-DE",
	}
	for in, want := range cases {
		got, conf := d.Detect(in)
		if got != want {
			t.Errorf("%q: got %s (%.2f) want %s", in, got, conf, want)
		}
	}
}

func TestDetectCandidatesRestrict(t *testing.T) {
	// Without candidates a French sentence is French; with candidates de/en
	// it must still resolve to one of those (here: whatever is closest) and
	// never something outside the list.
	open := &Detector{Default: "en-US"}
	if got, _ := open.Detect("Le serveur de base de données est inaccessible depuis dix minutes"); got != "fr-FR" {
		t.Errorf("open detect: %s", got)
	}
	restricted := &Detector{Candidates: []string{"de", "en"}, Default: "en-US"}
	got, _ := restricted.Detect("Le serveur de base de données est inaccessible depuis dix minutes")
	if got != "en-US" && got != "de-DE" {
		t.Errorf("restricted detect leaked: %s", got)
	}
	// Candidate with region wins tag completion.
	at := &Detector{Candidates: []string{"de-AT", "en-GB"}, Default: "de-AT"}
	if got, _ := at.Detect("The disk is full on the server"); got != "en-GB" {
		t.Errorf("region completion: %s", got)
	}
	if got, _ := at.Detect("Die Festplatte ist voll"); got != "de-AT" {
		t.Errorf("region completion de: %s", got)
	}
}

func TestSegments(t *testing.T) {
	d := &Detector{Candidates: []string{"de-DE", "en-US"}, Default: "de-DE"}
	text := "Northplane Alarm. Schweregrad kritisch. CPU load is very high on the web server. Drücken Sie die 4 zum Quittieren."
	segs := d.Segments(text)
	if len(segs) != 3 {
		t.Fatalf("segments: %+v", segs)
	}
	if segs[0].Lang != "de-DE" || segs[1].Lang != "en-US" || segs[2].Lang != "de-DE" {
		t.Fatalf("segment languages: %+v", segs)
	}
	if segs[0].Text != "Northplane Alarm. Schweregrad kritisch." {
		t.Errorf("merge of short sentences: %q", segs[0].Text)
	}
	if segs[1].Text != "CPU load is very high on the web server." {
		t.Errorf("english segment: %q", segs[1].Text)
	}
	// single language → one segment
	one := d.Segments("Alles in Ordnung. Keine offenen Alarme.")
	if len(one) != 1 || one[0].Lang != "de-DE" {
		t.Fatalf("single: %+v", one)
	}
	if d.Segments("") != nil {
		t.Fatal("empty")
	}
}

func TestSplitSentences(t *testing.T) {
	got := splitSentences("Wert 12.5 überschritten. Siehe z.B. Doku! Fertig? Ja")
	want := []string{"Wert 12.5 überschritten.", "Siehe z.B. Doku!", "Fertig?", "Ja"}
	if len(got) != len(want) {
		t.Fatalf("got %q", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%d: %q != %q", i, got[i], want[i])
		}
	}
}
