package main

import "strings"

// Names holds the consistent casings derived from a single user-supplied
// resource name. A new monitored-resource type touches Go (PascalCase
// type, snake storage kind constant), the bundle vocabulary (PascalCase
// kind), REST routes (kebab plural path), the storage kind value (kebab),
// and the frontend (PascalCase interface, kebab resourceApi base). Deriving
// them all from one input keeps the generated stubs internally consistent.
type Names struct {
	Pascal       string // "ContactGroup" — Go type, bundle kind, TS interface
	PascalPlural string // "ContactGroups"
	Camel        string // "contactGroup" — TS field-ish / JS identifiers
	Kebab        string // "contact-group" — storage kind value, REST singular
	KebabPlural  string // "contact-groups" — REST path, resourceApi base
	Snake        string // "contact_group" — SQL-ish / file names
	Title        string // "Contact Group" — human label
	ConstName    string // "KindContactGroup" — storage kind constant identifier
}

// ParseName accepts Foo / foo / foo-bar / fooBar / foo_bar and derives a
// consistent set of casings. The input is first split into words on
// case boundaries and the usual separators, then recombined.
func ParseName(in string) Names {
	words := splitWords(in)
	pascal := camelOrPascal(words, true)
	n := Names{
		Pascal:       pascal,
		PascalPlural: pluralize(pascal),
		Camel:        camelOrPascal(words, false),
		Kebab:        strings.Join(words, "-"),
		Snake:        strings.Join(words, "_"),
		Title:        titleWords(words),
		ConstName:    "Kind" + pascal,
	}
	n.KebabPlural = pluralize(n.Kebab)
	return n
}

// splitWords breaks an identifier into lowercase words on separators and
// camelCase / PascalCase boundaries. "ContactGroup" → [contact group],
// "foo-bar" → [foo bar], "HTTPProxy" → [http proxy].
func splitWords(in string) []string {
	// First normalise explicit separators to spaces.
	repl := strings.NewReplacer("-", " ", "_", " ", ".", " ", "/", " ")
	in = repl.Replace(in)

	var words []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			words = append(words, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	runes := []rune(in)
	for i, r := range runes {
		switch {
		case r == ' ':
			flush()
		case isUpper(r):
			// Boundary before an uppercase that starts a new word, e.g. the
			// "G" in "contactGroup" or the "P" in "HTTPProxy" (upper followed
			// by lower while the previous run was uppercase).
			prevLower := i > 0 && isLower(runes[i-1])
			nextLower := i+1 < len(runes) && isLower(runes[i+1])
			if len(cur) > 0 && (prevLower || nextLower) {
				flush()
			}
			cur = append(cur, r)
		default:
			cur = append(cur, r)
		}
	}
	flush()
	if len(words) == 0 {
		return []string{"resource"}
	}
	return words
}

func camelOrPascal(words []string, leadingUpper bool) string {
	var b strings.Builder
	for i, w := range words {
		if i == 0 && !leadingUpper {
			b.WriteString(w)
			continue
		}
		b.WriteString(capitalize(w))
	}
	return b.String()
}

func titleWords(words []string) string {
	out := make([]string, len(words))
	for i, w := range words {
		out[i] = capitalize(w)
	}
	return strings.Join(out, " ")
}

// pluralize applies a small set of English rules — enough for resource
// names. The generator prints the derived plural in its plan so a wrong
// guess is visible and trivially overridden by hand.
func pluralize(s string) string {
	if s == "" {
		return s
	}
	lower := strings.ToLower(s)
	switch {
	case strings.HasSuffix(lower, "s"), strings.HasSuffix(lower, "x"),
		strings.HasSuffix(lower, "z"), strings.HasSuffix(lower, "ch"),
		strings.HasSuffix(lower, "sh"):
		return s + "es"
	case strings.HasSuffix(lower, "y") && len(s) >= 2 && !isVowel(rune(lower[len(lower)-2])):
		return s[:len(s)-1] + "ies"
	default:
		return s + "s"
	}
}

func capitalize(w string) string {
	if w == "" {
		return w
	}
	r := []rune(w)
	r[0] = toUpper(r[0])
	return string(r)
}

func isUpper(r rune) bool { return r >= 'A' && r <= 'Z' }
func isLower(r rune) bool { return r >= 'a' && r <= 'z' }
func toUpper(r rune) rune {
	if isLower(r) {
		return r - ('a' - 'A')
	}
	return r
}
func isVowel(r rune) bool { return strings.ContainsRune("aeiou", r) }
