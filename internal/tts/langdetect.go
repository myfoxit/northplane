package tts

import (
	"strings"
	"sync"
	"unicode"

	"github.com/abadojack/whatlanggo"
)

// Detector identifies the language of alarm text. Alarm messages are
// short ("CPU load high on web01", "Festplatte voll"), which defeats
// plain n-gram detectors, so two signals are combined:
//
//   - a stop-word / domain-word scorer (function words plus the handful
//     of monitoring words — failed, down, voll, ausgefallen — that
//     appear in alarm titles), decisive for short texts;
//   - whatlanggo's trigram profiles, restricted to the candidate
//     languages, for everything longer.
//
// Restricting candidates (Detect.Languages) is the single most effective
// accuracy lever: choosing between German and English is easy, between
// 80 languages is not.
type Detector struct {
	// Candidates are BCP-47 tags or prefixes; empty = unrestricted.
	Candidates []string
	// MinConfidence below which Default is returned (0 → 0.35).
	MinConfidence float64
	// Default tag when detection is undecided (e.g. "de-DE").
	Default string
}

// Segment is a run of text in one language.
type Segment struct {
	Text string `json:"text"`
	Lang string `json:"lang"`
}

var (
	isoOnce   sync.Once
	isoToLang map[string]whatlanggo.Lang
)

func isoMap() map[string]whatlanggo.Lang {
	isoOnce.Do(func() {
		isoToLang = map[string]whatlanggo.Lang{}
		for l := whatlanggo.Afr; l <= whatlanggo.Zul; l++ {
			if c := l.Iso6391(); c != "" {
				isoToLang[c] = l
			}
		}
		isoToLang["nb"] = whatlanggo.Nob // whatlanggo maps Nob to "nb"; accept "no" too
		isoToLang["no"] = whatlanggo.Nob
	})
	return isoToLang
}

// defaultRegion completes a bare language code to the region most
// engines expect as a voice locale.
var defaultRegion = map[string]string{
	"en": "en-US", "de": "de-DE", "fr": "fr-FR", "es": "es-ES", "it": "it-IT", "nl": "nl-NL",
	"pt": "pt-PT", "pl": "pl-PL", "cs": "cs-CZ", "sk": "sk-SK", "sv": "sv-SE", "da": "da-DK",
	"nb": "nb-NO", "no": "nb-NO", "fi": "fi-FI", "tr": "tr-TR", "hu": "hu-HU", "ro": "ro-RO",
	"ru": "ru-RU", "uk": "uk-UA", "el": "el-GR", "bg": "bg-BG", "hr": "hr-HR", "sl": "sl-SI",
	"sr": "sr-RS", "lt": "lt-LT", "lv": "lv-LV", "et": "et-EE", "ja": "ja-JP", "zh": "zh-CN",
	"ko": "ko-KR", "ar": "ar-SA", "he": "he-IL", "hi": "hi-IN", "th": "th-TH", "vi": "vi-VN",
	"id": "id-ID", "ms": "ms-MY", "ca": "ca-ES", "af": "af-ZA", "fa": "fa-IR",
}

// FullTag completes a language code or tag to a BCP-47 tag with region,
// preferring a matching candidate / default tag ("de" → "de-AT" when
// the profile lists de-AT).
func (d *Detector) FullTag(code string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		return d.Default
	}
	if strings.ContainsAny(code, "-_") {
		return strings.Replace(code, "_", "-", 1)
	}
	p := langPrefix(code)
	for _, c := range d.Candidates {
		if langPrefix(c) == p && strings.ContainsAny(c, "-_") {
			return strings.Replace(c, "_", "-", 1)
		}
	}
	if langPrefix(d.Default) == p && strings.ContainsAny(d.Default, "-_") {
		return d.Default
	}
	if full, ok := defaultRegion[p]; ok {
		return full
	}
	return code
}

func (d *Detector) minConf() float64 {
	if d.MinConfidence > 0 {
		return d.MinConfidence
	}
	return 0.35
}

func (d *Detector) candidatePrefixes() map[string]bool {
	if len(d.Candidates) == 0 {
		return nil
	}
	m := map[string]bool{}
	for _, c := range d.Candidates {
		if p := langPrefix(c); p != "" {
			m[p] = true
		}
	}
	return m
}

// Detect returns the language tag of text and a confidence 0–1. The
// Default is returned (with confidence 0) when nothing reliable is found.
func (d *Detector) Detect(text string) (string, float64) {
	code, conf := d.detectCode(text)
	if code == "" {
		return d.Default, 0
	}
	return d.FullTag(code), conf
}

// detectCode is Detect without tag completion (2-letter code or "").
func (d *Detector) detectCode(text string) (string, float64) {
	cands := d.candidatePrefixes()
	allowed := func(code string) bool { return cands == nil || cands[code] }

	// 1. stop-word scorer
	swCode, swConf := stopwordScore(text, cands)

	// 2. trigram profiles
	opts := whatlanggo.Options{}
	if cands != nil {
		opts.Whitelist = map[whatlanggo.Lang]bool{}
		for p := range cands {
			if l, ok := isoMap()[p]; ok {
				opts.Whitelist[l] = true
			}
		}
		if len(opts.Whitelist) == 0 {
			opts.Whitelist = nil // candidates unknown to whatlanggo: fall back to stop words only
		}
	}
	letters := countLetters(text)
	tgCode, tgConf := "", 0.0
	if letters >= 6 {
		info := whatlanggo.DetectWithOptions(text, opts)
		if info.Lang >= 0 {
			tgCode, tgConf = info.Lang.Iso6391(), info.Confidence
			if !allowed(tgCode) {
				tgCode, tgConf = "", 0
			}
		}
	}
	// Very short texts: the trigram verdict is noise unless it agrees.
	if letters < 20 && tgConf < 0.9 && swCode != "" {
		tgConf *= 0.5
	}

	switch {
	case swCode != "" && tgCode == swCode:
		if swConf > tgConf {
			return swCode, swConf
		}
		return tgCode, tgConf
	case swCode != "" && swConf >= 0.7 && tgConf < 0.95:
		return swCode, swConf
	case tgCode != "" && tgConf >= d.minConf():
		return tgCode, tgConf
	case swCode != "" && swConf >= d.minConf():
		return swCode, swConf
	}
	return "", 0
}

func countLetters(s string) int {
	n := 0
	for _, r := range s {
		if unicode.IsLetter(r) {
			n++
		}
	}
	return n
}

// Segments splits text into sentences, detects each and merges runs of
// the same language. Sentences too short to judge inherit the language
// of their neighbour (previous first), then the dominant language, then
// Default.
func (d *Detector) Segments(text string) []Segment {
	sentences := splitSentences(text)
	if len(sentences) == 0 {
		return nil
	}
	codes := make([]string, len(sentences))
	charsBy := map[string]int{}
	for i, s := range sentences {
		code, conf := d.detectCode(s)
		if code == "" {
			continue
		}
		// Short sentences ("Schweregrad kritisch.") are only decided by a
		// confident stop-word hit; otherwise they inherit a neighbour.
		short := countLetters(s) < 12 || countWords(s) < 3
		if (!short && conf >= d.minConf()) || conf >= 0.7 {
			codes[i] = code
			charsBy[code] += len(s)
		}
	}
	dominant := langPrefix(d.Default)
	best := 0
	for c, n := range charsBy {
		if n > best || (n == best && c < dominant) {
			dominant, best = c, n
		}
	}
	if dominant == "" {
		dominant = langPrefix(d.Default)
	}
	// inherit: previous decided, else next decided, else dominant
	for i := range codes {
		if codes[i] != "" {
			continue
		}
		for j := i - 1; j >= 0; j-- {
			if codes[j] != "" {
				codes[i] = codes[j]
				break
			}
		}
		if codes[i] != "" {
			continue
		}
		for j := i + 1; j < len(codes); j++ {
			if codes[j] != "" {
				codes[i] = codes[j]
				break
			}
		}
		if codes[i] == "" {
			codes[i] = dominant
		}
	}
	var out []Segment
	for i, s := range sentences {
		tag := d.FullTag(codes[i])
		if len(out) > 0 && out[len(out)-1].Lang == tag {
			out[len(out)-1].Text += " " + s
			continue
		}
		out = append(out, Segment{Text: s, Lang: tag})
	}
	return out
}

// splitSentences cuts at sentence punctuation followed by whitespace
// (or end of text) and at line breaks.
func splitSentences(s string) []string {
	var out []string
	var cur strings.Builder
	runes := []rune(strings.TrimSpace(s))
	for i, r := range runes {
		cur.WriteRune(r)
		end := false
		switch r {
		case '\n':
			end = true
		case '.', '!', '?':
			// end of sentence when followed by space/end — except for
			// dotted abbreviations like "z.B." / "e.g." (letter, dot, letter, dot).
			if i+1 >= len(runes) || unicode.IsSpace(runes[i+1]) {
				end = true
				if r == '.' && i >= 2 && unicode.IsLetter(runes[i-1]) && runes[i-2] == '.' {
					end = false
				}
			}
		}
		if end {
			if t := strings.TrimSpace(cur.String()); t != "" {
				out = append(out, t)
			}
			cur.Reset()
		}
	}
	if t := strings.TrimSpace(cur.String()); t != "" {
		out = append(out, t)
	}
	return out
}

func countWords(s string) int {
	n := 0
	in := false
	for _, r := range s {
		if unicode.IsLetter(r) {
			if !in {
				n++
				in = true
			}
		} else {
			in = false
		}
	}
	return n
}

// --- stop-word scorer --------------------------------------------------------

// stopwords: function words plus frequent monitoring vocabulary per
// language. Weighted 1 each; words shared between languages (e.g. "in",
// "server") simply score for all of them and cancel out.
var stopwords = map[string][]string{
	"en": {"the", "and", "is", "are", "on", "of", "to", "in", "for", "with", "at", "from", "has", "have", "not", "this", "that",
		"was", "were", "be", "been", "by", "an", "it", "its", "or", "no", "down", "up", "failed", "failure", "error", "high", "low",
		"unreachable", "please", "press", "alert", "alarm", "warning", "critical", "resolved", "acknowledge", "acknowledged",
		"disk", "full", "load", "memory", "service", "host", "check", "timeout", "since", "again", "new", "open", "closed",
		"severity", "minutes", "hours", "seconds", "above", "below", "threshold", "usage", "space", "left", "available", "unavailable"},
	"de": {"der", "die", "das", "und", "ist", "sind", "auf", "von", "zu", "im", "in", "für", "mit", "bei", "aus", "nicht", "ein", "eine",
		"einer", "eines", "den", "dem", "des", "wurde", "wird", "werden", "hat", "haben", "sich", "oder", "seit", "über", "unter",
		"nach", "bitte", "drücken", "alarm", "warnung", "kritisch", "fehler", "ausgefallen", "erreichbar", "voll", "hoch", "niedrig",
		"quittiert", "quittieren", "gelöst", "ausgelöst", "störung", "meldung", "dienst", "festplatte", "speicher", "ausfall",
		"schweregrad", "minuten", "stunden", "sekunden", "mehr", "weniger", "als", "noch", "keine", "kein", "wieder", "neu", "offen",
		"geschlossen", "belegt", "frei", "verfügbar", "prüfung", "zeitüberschreitung", "taste", "sie", "wurden", "läuft"},
	"fr": {"le", "la", "les", "et", "est", "sont", "sur", "de", "des", "du", "dans", "pour", "avec", "pas", "ne", "un", "une", "au", "aux",
		"ce", "cette", "qui", "que", "par", "erreur", "alerte", "alarme", "critique", "panne", "échec", "serveur", "disque", "plein",
		"élevé", "depuis", "veuillez", "appuyez", "touche", "mémoire", "service", "indisponible", "résolu", "acquitté"},
	"es": {"el", "la", "los", "las", "y", "es", "son", "en", "de", "del", "para", "con", "no", "un", "una", "al", "por", "que", "este",
		"esta", "error", "alerta", "alarma", "crítico", "crítica", "fallo", "servidor", "disco", "lleno", "alto", "desde", "pulse",
		"tecla", "memoria", "servicio", "caído", "resuelto", "reconocido"},
	"it": {"il", "lo", "la", "gli", "le", "e", "è", "sono", "su", "di", "del", "della", "per", "con", "non", "un", "una", "al", "da",
		"che", "questo", "errore", "allarme", "critico", "guasto", "server", "disco", "pieno", "alto", "premere", "tasto", "memoria",
		"servizio", "risolto", "riconosciuto"},
	"nl": {"de", "het", "een", "en", "is", "zijn", "op", "van", "voor", "met", "niet", "in", "aan", "bij", "dat", "dit", "die", "wordt",
		"werd", "fout", "alarm", "waarschuwing", "kritiek", "storing", "server", "schijf", "vol", "hoog", "druk", "toets", "geheugen",
		"dienst", "opgelost", "bevestigd"},
	"pt": {"o", "a", "os", "as", "e", "é", "são", "em", "de", "do", "da", "dos", "das", "para", "com", "não", "um", "uma", "no", "na",
		"por", "que", "erro", "alerta", "alarme", "crítico", "falha", "servidor", "disco", "cheio", "alto", "pressione", "tecla",
		"memória", "serviço", "resolvido", "reconhecido"},
	"pl": {"i", "jest", "są", "na", "w", "z", "do", "nie", "się", "to", "że", "dla", "od", "po", "przez", "błąd", "alarm", "ostrzeżenie",
		"krytyczny", "awaria", "serwer", "dysk", "pełny", "wysoki", "naciśnij", "pamięć", "usługa"},
	"cs": {"a", "je", "jsou", "na", "v", "z", "do", "ne", "se", "to", "že", "pro", "od", "po", "chyba", "alarm", "varování", "kritický",
		"porucha", "server", "disk", "plný", "vysoký", "stiskněte", "paměť", "služba"},
	"sv": {"och", "är", "på", "av", "för", "med", "inte", "en", "ett", "den", "det", "till", "från", "som", "fel", "larm", "varning",
		"kritisk", "server", "disk", "full", "hög", "tryck", "minne", "tjänst"},
	"da": {"og", "er", "på", "af", "for", "med", "ikke", "en", "et", "den", "det", "til", "fra", "som", "fejl", "alarm", "advarsel",
		"kritisk", "server", "disk", "fuld", "høj", "tryk", "hukommelse", "tjeneste"},
	"nb": {"og", "er", "på", "av", "for", "med", "ikke", "en", "et", "den", "det", "til", "fra", "som", "feil", "alarm", "advarsel",
		"kritisk", "server", "disk", "full", "høy", "trykk", "minne", "tjeneste"},
	"fi": {"ja", "on", "ovat", "ei", "se", "että", "tämä", "kanssa", "virhe", "hälytys", "varoitus", "kriittinen", "palvelin",
		"levy", "täynnä", "korkea", "paina", "muisti", "palvelu"},
	"tr": {"ve", "bir", "bu", "için", "ile", "değil", "var", "yok", "hata", "alarm", "uyarı", "kritik", "sunucu", "disk", "dolu",
		"yüksek", "basın", "bellek", "hizmet"},
	"hu": {"és", "a", "az", "egy", "van", "nem", "ez", "hogy", "hiba", "riasztás", "figyelmeztetés", "kritikus", "szerver", "lemez",
		"tele", "magas", "nyomja", "memória", "szolgáltatás"},
	"ro": {"și", "este", "sunt", "pe", "de", "la", "cu", "nu", "un", "o", "în", "din", "pentru", "eroare", "alertă", "alarmă", "critic",
		"server", "disc", "plin", "ridicat", "apăsați", "memorie", "serviciu"}, //nolint:misspell // Romanian
}

var (
	stopOnce  sync.Once
	stopIndex map[string][]string // word → languages
)

func stopwordIndex() map[string][]string {
	stopOnce.Do(func() {
		stopIndex = map[string][]string{}
		for lang, words := range stopwords {
			for _, w := range words {
				stopIndex[w] = append(stopIndex[w], lang)
			}
		}
	})
	return stopIndex
}

// stopwordScore returns the best-scoring language and a confidence.
func stopwordScore(text string, cands map[string]bool) (string, float64) {
	idx := stopwordIndex()
	score := map[string]float64{}
	total := 0
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) }) {
		total++
		langs, ok := idx[w]
		if !ok {
			continue
		}
		// a word shared by n languages is worth 1/n to each
		weight := 1.0 / float64(len(langs))
		for _, l := range langs {
			if cands == nil || cands[l] {
				score[l] += weight
			}
		}
	}
	best, second := "", ""
	for l, s := range score {
		if best == "" || s > score[best] || (s == score[best] && l < best) {
			second = best
			best = l
		} else if second == "" || s > score[second] {
			second = l
		}
	}
	if best == "" || score[best] == 0 {
		return "", 0
	}
	b := score[best]
	s := 0.0
	if second != "" {
		s = score[second]
	}
	switch {
	case b >= 2 && b >= 2*s:
		return best, 0.9
	case b >= 1 && s == 0:
		return best, 0.7
	case b > s:
		return best, 0.5
	}
	return "", 0
}
