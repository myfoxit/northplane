package tts

import (
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/northplane/northplane/internal/model"
)

// Normalizer rewrites alarm text into something a speech engine reads
// the way an operator would say it. The pipeline, in order:
//
//  1. markup/emoji cleanup
//  2. operator lexicon (literal, whole-word)
//  3. operator regex rules
//  4. built-in IT-operations lexicon (per language)
//  5. spell-out list
//  6. URLs, e-mail addresses
//  7. token pass: IP addresses / dotted versions, dates and times,
//     identifiers (web01, np-02), acronyms, units after numbers, numbers
//     (auto/digits/words), symbols (% & = → < > # @ ~ € $ …), paths
//  8. whitespace/punctuation tidy
//
// Everything is plain text in, plain text out — no SSML — so it works
// identically for every engine, including a local piper or flite.
type Normalizer struct {
	cfg     model.TTSNormalize
	regex   []compiledRule
	lexicon []compiledLex
}

type compiledRule struct {
	re      *regexp.Regexp
	replace string
}

type compiledLex struct {
	from, to  string
	matchCase bool
	substring bool
}

// NewNormalizer compiles the profile's rules; invalid regular expressions
// are reported (used by resource validation on save).
func NewNormalizer(cfg model.TTSNormalize) (*Normalizer, error) {
	n := &Normalizer{cfg: cfg}
	for i, r := range cfg.Regex {
		if strings.TrimSpace(r.Pattern) == "" {
			continue
		}
		re, err := regexp.Compile(r.Pattern)
		if err != nil {
			return nil, fmt.Errorf("regex rule %d (%q): %w", i+1, r.Pattern, err)
		}
		n.regex = append(n.regex, compiledRule{re: re, replace: r.Replace})
	}
	for _, e := range cfg.Lexicon {
		if e.From == "" {
			continue
		}
		n.lexicon = append(n.lexicon, compiledLex{from: e.From, to: e.To,
			matchCase: e.MatchCase, substring: e.Substring})
	}
	return n, nil
}

// Apply normalises text for the given language (BCP-47 tag or prefix).
func (n *Normalizer) Apply(text, lang string) string {
	if n == nil || n.cfg.Disabled {
		return tidy(cleanup(text))
	}
	w := wordsFor(lang)
	s := cleanup(text)

	for _, e := range n.lexicon {
		s = replaceLex(s, e)
	}
	for _, r := range n.regex {
		s = r.re.ReplaceAllString(s, r.replace)
	}
	if !n.cfg.NoBuiltinLexicon {
		s = applyBuiltinLexicon(s, lang)
	}
	for _, tok := range n.cfg.SpellOut {
		if tok = strings.TrimSpace(tok); tok != "" {
			s = replaceLex(s, compiledLex{from: tok, to: spell(tok)})
		}
	}
	s = n.rewriteURLs(s, w)
	s = rewriteEmails(s, w)
	s = n.tokenPass(s, lang, w)
	return tidy(s)
}

// --- step 1: cleanup ------------------------------------------------------

var (
	reTags       = regexp.MustCompile(`<[a-zA-Z/][^<>]{0,120}>`)
	reMarkdown   = regexp.MustCompile("[*]{1,3}([^*\n]{1,200}?)[*]{1,3}|`([^`\n]{1,200})`")
	reSpaces     = regexp.MustCompile(`[ \t\x{00A0}]+`)
	reLineBreaks = regexp.MustCompile(`\s*[\r\n]+\s*`)
	glyphs       = strings.NewReplacer("→", " -> ", "⇒", " => ", "≥", " >= ", "≤", " <= ", "≠", " != ",
		"×", " x ", "·", " ", "•", ", ", "’", "'", "‘", "'", "“", "\"", "”", "\"", "„", "\"")
)

func cleanup(s string) string {
	if strings.ContainsRune(s, '<') {
		s = reTags.ReplaceAllString(s, " ")
	}
	if strings.ContainsRune(s, '&') {
		s = html.UnescapeString(s)
	}
	if strings.ContainsAny(s, "*`") {
		s = reMarkdown.ReplaceAllString(s, "$1$2")
	}
	s = glyphs.Replace(s)
	// Drop emoji/pictographs and control characters; keep letters, digits,
	// punctuation, the symbols the token pass knows, and whitespace.
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t' || r == ' ':
			b.WriteRune(r)
		case unicode.IsControl(r):
			b.WriteByte(' ')
		case unicode.IsLetter(r), unicode.IsDigit(r), unicode.IsPunct(r), unicode.IsSpace(r):
			b.WriteRune(r)
		case strings.ContainsRune("+<=>|~^$€£¥°µ%&@#*/\\", r):
			b.WriteRune(r)
		default:
			b.WriteByte(' ') // emoji, box drawing, math symbols …
		}
	}
	s = reLineBreaks.ReplaceAllString(b.String(), ". ")
	return strings.TrimSpace(reSpaces.ReplaceAllString(s, " "))
}

// --- steps 2–5: lexicon ----------------------------------------------------

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// replaceLex performs a whole-word (or substring) literal replacement,
// case-insensitively unless matchCase. Word boundaries are "not a letter
// or digit", so keys like "z.B." or "np-01" work.
func replaceLex(s string, e compiledLex) string {
	if e.from == "" {
		return s
	}
	if e.substring && e.matchCase {
		return strings.ReplaceAll(s, e.from, e.to)
	}
	return replaceFold(s, e.from, e.to, !e.substring, e.matchCase)
}

func replaceFold(s, from, to string, wholeWord, matchCase bool) string {
	var b strings.Builder
	i := 0
	n := len(from)
	for i < len(s) {
		if i+n <= len(s) && (s[i:i+n] == from || (!matchCase && strings.EqualFold(s[i:i+n], from))) {
			ok := true
			if wholeWord {
				if i > 0 {
					if r, _ := utf8.DecodeLastRuneInString(s[:i]); isWordRune(r) {
						ok = false
					}
				}
				if ok && i+n < len(s) {
					if r, _ := utf8.DecodeRuneInString(s[i+n:]); isWordRune(r) {
						ok = false
					}
				}
			}
			if ok {
				b.WriteString(to)
				i += n
				continue
			}
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		b.WriteString(s[i : i+size])
		i += size
	}
	return b.String()
}

func applyBuiltinLexicon(s, lang string) string {
	tables := []map[string]string{builtinLexicon[""]}
	if t, ok := builtinLexicon[langPrefix(lang)]; ok {
		tables = append([]map[string]string{t}, tables...) // language table wins
	}
	// Keys with punctuation (z.B., w/o, i/o, ci/cd, np-agent) cannot be
	// found by word lookup — literal scan.
	lower := strings.ToLower(s)
	for _, t := range tables {
		for k, v := range t {
			if strings.ContainsAny(k, "./-") && strings.Contains(lower, k) {
				s = replaceFold(s, k, v, true, false)
			}
		}
	}
	return mapWords(s, func(word string) (string, bool) {
		lw := strings.ToLower(word)
		for _, t := range tables {
			if v, ok := t[lw]; ok {
				return v, true
			}
		}
		return word, false
	})
}

// mapWords calls fn for every maximal run of letters/digits (plus an
// in-word apostrophe) and substitutes the result.
func mapWords(s string, fn func(string) (string, bool)) string {
	var b strings.Builder
	runes := []rune(s)
	i := 0
	for i < len(runes) {
		if !isWordRune(runes[i]) {
			b.WriteRune(runes[i])
			i++
			continue
		}
		j := i + 1
		for j < len(runes) && (isWordRune(runes[j]) || (runes[j] == '\'' && j+1 < len(runes) && isWordRune(runes[j+1]))) {
			j++
		}
		word := string(runes[i:j])
		if v, ok := fn(word); ok {
			b.WriteString(v)
		} else {
			b.WriteString(word)
		}
		i = j
	}
	return b.String()
}

// spell returns a token spelled letter by letter ("np" → "N P"). Digits
// are kept as digits (separated).
func spell(tok string) string {
	var parts []string
	for _, r := range tok {
		switch {
		case unicode.IsLetter(r):
			parts = append(parts, string(unicode.ToUpper(r)))
		case unicode.IsDigit(r):
			parts = append(parts, string(r))
		}
	}
	return strings.Join(parts, " ")
}

// --- step 6: URLs and e-mail -------------------------------------------------

var (
	reURL   = regexp.MustCompile(`(?i)\b(?:https?|ftp|wss?)://[^\s<>"'\)\]]+`)
	reEmail = regexp.MustCompile(`[\pL\pN._%+-]+@[\pL\pN.-]+\.[\pL]{2,}`)
)

func (n *Normalizer) rewriteURLs(s string, w langWords) string {
	if !strings.Contains(s, "://") {
		return s
	}
	mode := n.cfg.URLs
	return reURL.ReplaceAllStringFunc(s, func(u string) string {
		switch mode {
		case "keep":
			return u
		case "drop":
			return ""
		}
		// host: strip scheme, credentials, port, path
		h := u[strings.Index(u, "://")+3:]
		if i := strings.IndexAny(h, "/?#"); i >= 0 {
			h = h[:i]
		}
		if i := strings.LastIndex(h, "@"); i >= 0 {
			h = h[i+1:]
		}
		if i := strings.Index(h, ":"); i >= 0 {
			h = h[:i]
		}
		return strings.Join(strings.Split(h, "."), " "+w.dot+" ")
	})
}

func rewriteEmails(s string, w langWords) string {
	if !strings.Contains(s, "@") {
		return s
	}
	return reEmail.ReplaceAllStringFunc(s, func(m string) string {
		user, host, _ := strings.Cut(m, "@")
		user = strings.NewReplacer(".", " "+w.dot+" ", "_", " ", "-", " ").Replace(user)
		host = strings.Join(strings.Split(host, "."), " "+w.dot+" ")
		return user + " " + w.at + " " + host
	})
}

// --- step 7: token pass --------------------------------------------------------

var (
	reIPv4        = regexp.MustCompile(`^\d{1,3}(\.\d{1,3}){3}(/\d{1,2})?$`)
	reDotted      = regexp.MustCompile(`^v?\d+(\.\d+){2,}$`)
	reDecimal     = regexp.MustCompile(`^\d+[.,]\d+$`)
	reInteger     = regexp.MustCompile(`^\d+$`)
	reTime        = regexp.MustCompile(`^\d{1,2}:\d{2}(:\d{2})?$`)
	reISODate     = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	reISODateTime = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})[T ](\d{1,2}:\d{2})(?::\d{2}(?:\.\d+)?)?(?:Z|[+-]\d{2}:?\d{2})?$`)
	reRange       = regexp.MustCompile(`^(\d+)-(\d+)$`)
	reOrdinalEN   = regexp.MustCompile(`^\d+(st|nd|rd|th)$`)
	rePhone       = regexp.MustCompile(`^\+\d[\d\-/]{6,}\d$`)
	reNumUnit     = regexp.MustCompile(`^(\d+(?:[.,]\d+)?)([^\d\s][^\s]*)$`)
)

// isInnerRune lists characters that bind tokens together when surrounded
// by token runes (IPs, hostnames, versions, paths, times, "+49…").
func isInnerRune(r rune) bool {
	return strings.ContainsRune("-._/:+°µ'", r)
}

// tokenStart reports whether a token may begin at runes[i].
func tokenStart(runes []rune, i int) bool {
	r := runes[i]
	if isWordRune(r) || r == '°' {
		return true
	}
	if i+1 >= len(runes) {
		return false
	}
	next := runes[i+1]
	return (r == '+' && unicode.IsDigit(next)) || (r == '/' && unicode.IsLetter(next))
}

type token struct {
	text string
	sep  bool // whitespace/punctuation between tokens
}

func tokenize(s string) []token {
	var toks []token
	runes := []rune(s)
	i := 0
	for i < len(runes) {
		if tokenStart(runes, i) {
			j := i + 1
			for j < len(runes) {
				c := runes[j]
				if isWordRune(c) {
					j++
					continue
				}
				if isInnerRune(c) && j+1 < len(runes) && isWordRune(runes[j+1]) {
					j++
					continue
				}
				break
			}
			toks = append(toks, token{text: string(runes[i:j])})
			i = j
			continue
		}
		j := i + 1
		for j < len(runes) && !tokenStart(runes, j) {
			j++
		}
		toks = append(toks, token{text: string(runes[i:j]), sep: true})
		i = j
	}
	return toks
}

func (n *Normalizer) tokenPass(s, lang string, w langWords) string {
	toks := tokenize(s)
	var b strings.Builder
	prevNumber := false // previous token was a bare number (unit lookahead)
	prevValue := 0.0
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if t.sep {
			nextIsDigit := i+1 < len(toks) && startsWithDigit(toks[i+1].text)
			b.WriteString(n.symbols(t.text, w, nextIsDigit))
			if strings.ContainsAny(t.text, ".,;:!?()[]{}\"") {
				prevNumber = false // punctuation breaks the number→unit link
			}
			continue
		}
		out, isNum, val := n.speakToken(t.text, lang, w, prevNumber, prevValue)
		b.WriteString(out)
		prevNumber, prevValue = isNum, val
	}
	return b.String()
}

func startsWithDigit(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	return unicode.IsDigit(r)
}

// speakToken renders one token. It reports whether the token was a
// plain number (so a following unit abbreviation can be expanded) and
// its value (singular/plural).
func (n *Normalizer) speakToken(tok, lang string, w langWords, prevNumber bool, prevValue float64) (string, bool, float64) {
	lower := strings.ToLower(tok)

	// "+49", "+5" → "plus 4 9" / "plus 5" (the phone rule below covers
	// longer numbers).
	if strings.HasPrefix(tok, "+") && !rePhone.MatchString(tok) {
		out, isNum, val := n.speakToken(tok[1:], lang, w, false, 0)
		return w.plus + " " + out, isNum, val
	}

	// A unit abbreviation right after a number: "20 ms", "512 MB", "21 °C".
	if prevNumber && n.cfg.Units != "native" {
		if u, ok := w.units[lower]; ok {
			if prevValue == 1 {
				return u[0], false, 0
			}
			return u[1], false, 0
		}
	}

	switch {
	case reInteger.MatchString(tok):
		return n.number(tok, lang), true, parseFloat(tok)
	case reDecimal.MatchString(tok):
		return localizeDecimal(tok, lang), true, parseFloat(strings.Replace(tok, ",", ".", 1))
	case reIPv4.MatchString(tok):
		if n.cfg.IPAddresses == "native" {
			return tok, false, 0
		}
		return n.dotted(tok, w), false, 0
	case reDotted.MatchString(tok):
		return n.dotted(tok, w), false, 0
	case reTime.MatchString(tok), reISODate.MatchString(tok):
		return tok, false, 0 // engines read 12:30 and 2026-08-23 natively
	case reISODateTime.MatchString(tok):
		return reISODateTime.ReplaceAllString(tok, "$1, $2"), false, 0
	case reRange.MatchString(tok):
		m := reRange.FindStringSubmatch(tok)
		return n.number(m[1], lang) + " " + w.to + " " + n.number(m[2], lang), false, 0
	case rePhone.MatchString(tok):
		return w.plus + " " + spell(tok[1:]), false, 0
	case reOrdinalEN.MatchString(tok) && langPrefix(lang) == "en":
		return tok, false, 0
	}

	// number glued to a unit: "20ms", "512MB", "95°C"
	if m := reNumUnit.FindStringSubmatch(tok); m != nil && n.cfg.Units != "native" {
		if u, ok := w.units[strings.ToLower(m[2])]; ok {
			num := n.number(m[1], lang)
			if strings.ContainsAny(m[1], ".,") {
				num = localizeDecimal(m[1], lang)
			}
			if parseFloat(strings.Replace(m[1], ",", ".", 1)) == 1 {
				return num + " " + u[0], false, 0
			}
			return num + " " + u[1], false, 0
		}
	}
	if strings.HasPrefix(tok, "°") { // "°C" as its own token
		if u, ok := w.units[lower]; ok {
			return u[1], false, 0
		}
		return w.degrees + " " + strings.TrimPrefix(tok, "°"), false, 0
	}

	// paths: /var/lib/x → "slash var slash lib slash x"
	if strings.HasPrefix(tok, "/") && n.cfg.Symbols != "native" {
		var out []string
		for _, p := range strings.Split(strings.Trim(tok, "/"), "/") {
			if p != "" {
				out = append(out, w.slash, n.identifier(p, lang))
			}
		}
		return strings.Join(out, " "), false, 0
	}

	// hyphen/underscore/slash-joined identifiers: np-01, db_master, r/w
	if strings.ContainsAny(tok, "-_/") && n.cfg.Identifiers != "keep" {
		parts := strings.FieldsFunc(tok, func(r rune) bool { return r == '-' || r == '_' || r == '/' })
		if strings.Count(tok, "/") == 1 && len(parts) == 2 && !strings.ContainsAny(tok, "-_") {
			if len(parts[0]) <= 2 && len(parts[1]) <= 2 {
				return spell(parts[0]) + " " + spell(parts[1]), false, 0 // r/w
			}
			// errors/min, requests/sec → "errors per minute"
			unit := parts[1]
			if u, ok := w.units[strings.ToLower(unit)]; ok {
				unit = u[0]
			} else {
				unit = n.identifier(unit, lang)
			}
			return n.identifier(parts[0], lang) + " " + w.per + " " + unit, false, 0
		}
		var out []string
		for _, p := range parts {
			out = append(out, n.identifier(p, lang))
		}
		return strings.Join(out, " "), false, 0
	}
	return n.identifier(tok, lang), false, 0
}

// identifier renders a single word-like token: mixed letter/digit runs
// are split (web01 → "web 0 1"), acronyms are spelled, the rest is kept.
func (n *Normalizer) identifier(tok, lang string) string {
	if tok == "" {
		return ""
	}
	hasLetter, hasDigit := false, false
	for _, r := range tok {
		if unicode.IsLetter(r) {
			hasLetter = true
		} else if unicode.IsDigit(r) {
			hasDigit = true
		}
	}
	switch {
	case hasLetter && hasDigit && n.cfg.Identifiers != "keep":
		if reOrdinalEN.MatchString(tok) && langPrefix(lang) == "en" {
			return tok
		}
		var out []string
		runes := []rune(tok)
		start := 0
		for i := 1; i <= len(runes); i++ {
			if i == len(runes) || unicode.IsDigit(runes[i]) != unicode.IsDigit(runes[start]) {
				run := string(runes[start:i])
				if unicode.IsDigit(runes[start]) {
					// digits inside identifiers: leading zero or ≥3 digits → digit by digit
					if strings.HasPrefix(run, "0") || len(run) >= 3 || n.cfg.Numbers == "digits" {
						out = append(out, spell(run))
					} else {
						out = append(out, n.number(run, lang))
					}
				} else {
					out = append(out, n.word(run))
				}
				start = i
			}
		}
		return strings.Join(out, " ")
	case hasDigit && !hasLetter:
		return n.number(tok, lang)
	}
	return n.word(tok)
}

// word applies the acronym heuristics to a pure-letter token.
func (n *Normalizer) word(tok string) string {
	if n.cfg.Acronyms == "off" {
		return tok
	}
	runes := []rune(tok)
	if len(runes) < 2 {
		return tok
	}
	// ALL-CAPS (optionally plural "s"): CPU, VMs — spelled when listed or
	// vowel-less; other upper-case words are left to the engine
	plural := false
	body := runes
	if len(runes) >= 3 && runes[len(runes)-1] == 's' {
		body = runes[:len(runes)-1]
		plural = true
	}
	allUpper := true
	for _, r := range body {
		if !unicode.IsUpper(r) {
			allUpper = false
			break
		}
	}
	if allUpper && len(body) >= 2 && len(body) <= 6 {
		up := string(body)
		if !spelledAcronyms[up] && hasVowel(strings.ToLower(up)) {
			return tok // a shouted word (DISK, ERROR, FEHLER) or an acronym the engine reads itself
		}
		if plural {
			return spell(up) + " s"
		}
		return spell(up)
	}
	// all-lower-case short tokens without a vowel: dhcp, pbx, xml
	lower := strings.ToLower(tok)
	if len(runes) <= 5 && lower == tok && !hasVowel(lower) {
		return spell(tok)
	}
	return tok
}

// number renders a digit string per the Numbers mode.
func (n *Normalizer) number(tok, lang string) string {
	digitsFrom := n.cfg.DigitsFrom
	if digitsFrom <= 0 {
		digitsFrom = 5
	}
	switch n.cfg.Numbers {
	case "native":
		return tok
	case "digits":
		return spell(tok)
	case "words":
		if len(tok) > 1 && tok[0] == '0' {
			return spell(tok)
		}
		if v, err := strconv.ParseInt(tok, 10, 64); err == nil && len(tok) <= 15 {
			if s, ok := NumberWords(v, lang); ok {
				return s
			}
		}
		fallthrough
	default: // auto
		if len(tok) > 1 && tok[0] == '0' {
			return spell(tok)
		}
		if len(tok) >= digitsFrom {
			return spell(tok)
		}
		return tok
	}
}

// dotted reads 10.0.0.1 / v1.2.3 / 10.0.0.0/24 with the language's "dot".
func (n *Normalizer) dotted(tok string, w langWords) string {
	prefix := ""
	if strings.HasPrefix(strings.ToLower(tok), "v") {
		prefix = "v "
		tok = tok[1:]
	}
	cidr := ""
	if i := strings.Index(tok, "/"); i >= 0 {
		cidr = " " + w.slash + " " + tok[i+1:]
		tok = tok[:i]
	}
	parts := strings.Split(tok, ".")
	for i, p := range parts {
		if (len(p) > 1 && p[0] == '0') || len(p) >= 4 {
			parts[i] = spell(p)
		}
	}
	return prefix + strings.Join(parts, " "+w.dot+" ") + cidr
}

// localizeDecimal writes the decimal separator the language expects.
func localizeDecimal(tok, lang string) string {
	switch langPrefix(lang) {
	case "de", "fr", "es", "it", "nl", "pt", "pl", "cs", "sv", "da", "nb", "no", "fi", "tr", "hu", "ro", "ru":
		return strings.Replace(tok, ".", ",", 1)
	}
	return strings.Replace(tok, ",", ".", 1)
}

func parseFloat(s string) float64 {
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

// symbols rewrites a separator run. nextIsDigit tells whether a number
// follows (for "#4711" → "number 4711", "-5" → "minus 5").
func (n *Normalizer) symbols(sep string, w langWords, nextIsDigit bool) string {
	if n.cfg.Symbols == "native" {
		return sep
	}
	s := strings.NewReplacer("->", " "+w.arrow+" ", "=>", " "+w.arrow+" ", "<-", " ",
		"<=", " "+w.less+" "+w.equals+" ", ">=", " "+w.greater+" "+w.equals+" ",
		"!=", " "+w.not+" "+w.equals+" ", "==", " "+w.equals+" ",
		"...", ". ", "…", ". ", "--", ", ", "—", ", ", "–", ", ").Replace(sep)
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '%':
			b.WriteString(" " + w.percent + " ")
		case '&':
			b.WriteString(" " + w.and + " ")
		case '+':
			b.WriteString(" " + w.plus + " ")
		case '=':
			b.WriteString(" " + w.equals + " ")
		case '<':
			b.WriteString(" " + w.less + " ")
		case '>':
			b.WriteString(" " + w.greater + " ")
		case '#':
			if nextIsDigit {
				b.WriteString(" " + w.number + " ")
			} else {
				b.WriteByte(' ')
			}
		case '@':
			b.WriteString(" " + w.at + " ")
		case '~':
			b.WriteString(" " + w.about + " ")
		case '€':
			b.WriteString(" " + w.euro + " ")
		case '$':
			b.WriteString(" " + w.dollar + " ")
		case '£':
			b.WriteString(" " + w.pound + " ")
		case '°':
			b.WriteString(" " + w.degrees + " ")
		case '-':
			if nextIsDigit {
				b.WriteString(" " + w.minus + " ")
			} else {
				b.WriteString(", ")
			}
		case '/':
			b.WriteString(" " + w.slash + " ")
		case '|', '(', ')', '[', ']', ';':
			b.WriteString(", ")
		case '_', '*', '\\', '^', '`', '"', '{', '}':
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// --- step 8: tidy -------------------------------------------------------------

var (
	reMultiSpace = regexp.MustCompile(`\s{2,}`)
	reSpacePunct = regexp.MustCompile(`\s+([,.;:!?])`)
	reDupComma   = regexp.MustCompile(`(,\s*){2,}`)
	reDupPeriod  = regexp.MustCompile(`(\.\s*){2,}`)
	rePunctPer   = regexp.MustCompile(`[,;:]\s*\.`)
	rePerComma   = regexp.MustCompile(`\.\s*,`)
	reLeadPunct  = regexp.MustCompile(`^[,.;:\s]+`)
)

func tidy(s string) string {
	s = reMultiSpace.ReplaceAllString(s, " ")
	s = reSpacePunct.ReplaceAllString(s, "$1")
	s = reDupComma.ReplaceAllString(s, ", ")
	s = rePunctPer.ReplaceAllString(s, ".")
	s = rePerComma.ReplaceAllString(s, ".")
	s = reDupPeriod.ReplaceAllString(s, ". ")
	s = reLeadPunct.ReplaceAllString(s, "")
	s = reMultiSpace.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)
	s = strings.TrimRight(s, ",;: ")
	if s != "" && !strings.ContainsRune(".!?", rune(s[len(s)-1])) {
		s += "."
	}
	return s
}
