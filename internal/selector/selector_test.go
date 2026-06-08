package selector

import (
	"reflect"
	"testing"
)

// TestParseValid covers the full grammar (SPEC §11.1): every operator,
// whitespace tolerance, the "==" alias, set operators with multiple
// values, exists/not-exists, multi-requirement conjunctions and the
// match-all empty selector. It asserts the parsed Requirements so the
// table doubles as a spec of how each form is normalised.
func TestParseValid(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []Requirement
	}{
		{
			name: "empty matches all",
			in:   "",
			want: nil,
		},
		{
			name: "whitespace only matches all",
			in:   "   \t  ",
			want: nil,
		},
		{
			name: "single equals",
			in:   "env=prod",
			want: []Requirement{{Key: "env", Op: OpEq, Values: []string{"prod"}}},
		},
		{
			name: "double equals alias",
			in:   "env==prod",
			want: []Requirement{{Key: "env", Op: OpEq, Values: []string{"prod"}}},
		},
		{
			name: "not equals",
			in:   "site!=wien",
			want: []Requirement{{Key: "site", Op: OpNeq, Values: []string{"wien"}}},
		},
		{
			name: "in set sorts values",
			in:   "role in (db,cache,web)",
			want: []Requirement{{Key: "role", Op: OpIn, Values: []string{"cache", "db", "web"}}},
		},
		{
			name: "notin set sorts values",
			in:   "role notin (web,db)",
			want: []Requirement{{Key: "role", Op: OpNotIn, Values: []string{"db", "web"}}},
		},
		{
			name: "in set single value",
			in:   "role in (db)",
			want: []Requirement{{Key: "role", Op: OpIn, Values: []string{"db"}}},
		},
		{
			name: "exists bare key",
			in:   "legacy",
			want: []Requirement{{Key: "legacy", Op: OpExists}},
		},
		{
			name: "not exists",
			in:   "!legacy",
			want: []Requirement{{Key: "legacy", Op: OpNotExists}},
		},
		{
			name: "spec example multi requirement AND",
			in:   "env=prod,role in (db,cache),!legacy,site!=wien",
			want: []Requirement{
				{Key: "env", Op: OpEq, Values: []string{"prod"}},
				{Key: "role", Op: OpIn, Values: []string{"cache", "db"}},
				{Key: "legacy", Op: OpNotExists},
				{Key: "site", Op: OpNeq, Values: []string{"wien"}},
			},
		},
		{
			name: "whitespace around operators tolerated",
			in:   "  env = prod ,  role   in  ( db , cache )  ",
			want: []Requirement{
				{Key: "env", Op: OpEq, Values: []string{"prod"}},
				{Key: "role", Op: OpIn, Values: []string{"cache", "db"}},
			},
		},
		{
			name: "key with special chars dot slash dash underscore digits",
			in:   "app.kubernetes.io/name-2_x=foo",
			want: []Requirement{{Key: "app.kubernetes.io/name-2_x", Op: OpEq, Values: []string{"foo"}}},
		},
		{
			name: "value with hyphen and dot",
			in:   "version=1.2.3-rc",
			want: []Requirement{{Key: "version", Op: OpEq, Values: []string{"1.2.3-rc"}}},
		},
		{
			name: "empty value after equals",
			in:   "env=",
			want: []Requirement{{Key: "env", Op: OpEq, Values: []string{""}}},
		},
		{
			name: "multiple exists",
			in:   "a,b,c",
			want: []Requirement{
				{Key: "a", Op: OpExists},
				{Key: "b", Op: OpExists},
				{Key: "c", Op: OpExists},
			},
		},
		{
			// Documented lenience: a trailing comma is tolerated (the loop
			// breaks on EOF after eating it) rather than rejected.
			name: "trailing comma tolerated",
			in:   "env=prod,",
			want: []Requirement{{Key: "env", Op: OpEq, Values: []string{"prod"}}},
		},
		{
			// Values are unquoted and read verbatim until ',' or ')', so an
			// internal space becomes part of a single value, not a separator.
			name: "set value with internal space is one value",
			in:   "role in (db cache)",
			want: []Requirement{{Key: "role", Op: OpIn, Values: []string{"db cache"}}},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, err := Parse(c.in)
			if err != nil {
				t.Fatalf("Parse(%q) unexpected error: %v", c.in, err)
			}
			if !reflect.DeepEqual(s.Requirements(), c.want) {
				t.Fatalf("Parse(%q) reqs = %+v, want %+v", c.in, s.Requirements(), c.want)
			}
		})
	}
}

// TestParseInvalid asserts every malformed-input branch returns an error
// (and an empty selector), so a broken selector never silently degrades to
// match-all.
func TestParseInvalid(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"bang without key", "!"},
		{"bang then space", "! "},
		{"leading comma no key", ","},
		{"double comma", "env=prod,,role=db"},
		{"bang equals without bang", "env!x"},     // '!' not followed by '='
		{"unknown operator word", "env xyz prod"}, // word operator that isn't in/notin
		{"in without open paren", "role in db,cache)"},
		{"in unterminated set", "role in (db,cache"}, // missing ')' → value() consumes rest, loop wants ',' or ')'
		{"notin without open paren", "role notin db"},
		{"key followed by stray operator char", "env @ prod"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, err := Parse(c.in)
			if err == nil {
				t.Fatalf("Parse(%q) = %+v, want error", c.in, s.Requirements())
			}
			if !s.Empty() || s.Requirements() != nil {
				t.Fatalf("Parse(%q) on error must return zero Selector, got %+v", c.in, s)
			}
		})
	}
}

// TestMatches is exhaustive over operators against representative label
// maps, including the AND semantics, negation edge cases (key absent),
// empty-selector-matches-all and special-character values.
func TestMatches(t *testing.T) {
	labels := map[string]string{
		"env":     "prod",
		"role":    "db",
		"site":    "wien",
		"version": "1.2.3-rc",
		"empty":   "",
	}

	cases := []struct {
		name     string
		selector string
		labels   map[string]string
		want     bool
	}{
		// equals / ==
		{"eq match", "env=prod", labels, true},
		{"eq mismatch", "env=stage", labels, false},
		{"eq alias match", "env==prod", labels, true},
		{"eq missing key", "team=core", labels, false},
		{"eq empty value matches empty label", "empty=", labels, true},
		{"eq empty value mismatch non-empty", "env=", labels, false},

		// not equals: true when absent OR different
		{"neq different value", "site!=graz", labels, true},
		{"neq same value", "site!=wien", labels, false},
		{"neq missing key is true", "team!=core", labels, true},

		// in
		{"in match", "role in (db,cache)", labels, true},
		{"in no match", "role in (cache,web)", labels, false},
		{"in missing key", "team in (a,b)", labels, false},

		// notin: true when absent OR not in the set
		{"notin not in set", "role notin (cache,web)", labels, true},
		{"notin in set", "role notin (db,cache)", labels, false},
		{"notin missing key is true", "team notin (a,b)", labels, true},

		// exists / not exists
		{"exists present", "env", labels, true},
		{"exists absent", "team", labels, false},
		{"exists on empty-valued key", "empty", labels, true},
		{"not exists absent", "!team", labels, true},
		{"not exists present", "!env", labels, false},

		// multi-requirement AND
		{"AND all satisfied", "env=prod,role in (db,cache),!legacy,site!=wien-x", labels, true},
		{"AND one fails", "env=prod,role=cache", labels, false},
		{"AND not-exists fails because present", "env=prod,!role", labels, false},

		// special-char value
		{"special char value match", "version=1.2.3-rc", labels, true},

		// empty selector matches everything
		{"empty selector matches non-empty labels", "", labels, true},
		{"empty selector matches empty labels", "", map[string]string{}, true},
		{"empty selector matches nil labels", "", nil, true},

		// requirement against nil/empty label map
		{"eq against nil labels", "env=prod", nil, false},
		{"not-exists against nil labels true", "!env", nil, true},
		{"neq against nil labels true", "env!=prod", nil, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := MustParse(c.selector)
			if got := s.Matches(c.labels); got != c.want {
				t.Fatalf("MustParse(%q).Matches(%v) = %v, want %v", c.selector, c.labels, got, c.want)
			}
		})
	}
}

// TestEmpty verifies the Empty predicate is the inverse of having any
// requirement, since callers use it to short-circuit match-all.
func TestEmpty(t *testing.T) {
	if !MustParse("").Empty() {
		t.Fatal("empty string selector should be Empty()")
	}
	if !MustParse("   ").Empty() {
		t.Fatal("whitespace selector should be Empty()")
	}
	if MustParse("env=prod").Empty() {
		t.Fatal("non-empty selector should not be Empty()")
	}
}

// TestString round-trips the trimmed source text used for display/keys.
func TestString(t *testing.T) {
	cases := map[string]string{
		"  env=prod  ": "env=prod",
		"":             "",
		"a,b":          "a,b",
	}
	for in, want := range cases {
		if got := MustParse(in).String(); got != want {
			t.Fatalf("MustParse(%q).String() = %q, want %q", in, got, want)
		}
	}
}

// TestMustParsePanics ensures MustParse fails loudly on malformed static
// selectors instead of returning a silent match-all.
func TestMustParsePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("MustParse on invalid input should panic")
		}
	}()
	MustParse("env!x")
}

// TestRequirementsIsReadOnlySnapshot guards the invariant that callers
// receive the live slice; the test simply confirms identity/stability of
// the returned terms rather than mutating shared state.
func TestRequirementsStable(t *testing.T) {
	s := MustParse("env=prod,role in (db,cache)")
	a := s.Requirements()
	b := s.Requirements()
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("Requirements() not stable: %+v vs %+v", a, b)
	}
	if len(a) != 2 {
		t.Fatalf("expected 2 requirements, got %d", len(a))
	}
}
