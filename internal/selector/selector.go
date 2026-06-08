// Package selector implements the unified label-selector syntax used in
// query parameters, silences, downtimes, role scopes and dashboards
// (SPEC §11.1): `env=prod,role in (db,cache),!legacy,site!=wien`.
//
// Grammar (comma = AND):
//
//	selector   = requirement *("," requirement)
//	requirement= KEY "=" VALUE | KEY "==" VALUE | KEY "!=" VALUE
//	           | KEY "in" "(" VALUE *("," VALUE) ")"
//	           | KEY "notin" "(" VALUE *("," VALUE) ")"
//	           | KEY            (exists)
//	           | "!" KEY        (not exists)
package selector

import (
	"fmt"
	"sort"
	"strings"
)

// Op is a requirement operator.
type Op string

const (
	OpEq        Op = "="
	OpNeq       Op = "!="
	OpIn        Op = "in"
	OpNotIn     Op = "notin"
	OpExists    Op = "exists"
	OpNotExists Op = "!exists"
)

// Requirement is one AND-term.
type Requirement struct {
	Key    string
	Op     Op
	Values []string // sorted; 1 value for =/!=
}

// Selector is a conjunction of requirements.
type Selector struct {
	reqs []Requirement
	str  string
}

// Empty reports whether the selector matches everything.
func (s Selector) Empty() bool { return len(s.reqs) == 0 }

// Requirements exposes the parsed terms (read-only).
func (s Selector) Requirements() []Requirement { return s.reqs }

func (s Selector) String() string { return s.str }

// Matches evaluates the selector against a label set.
func (s Selector) Matches(labels map[string]string) bool {
	for _, r := range s.reqs {
		v, ok := labels[r.Key]
		switch r.Op {
		case OpExists:
			if !ok {
				return false
			}
		case OpNotExists:
			if ok {
				return false
			}
		case OpEq:
			if !ok || v != r.Values[0] {
				return false
			}
		case OpNeq:
			if ok && v == r.Values[0] {
				return false
			}
		case OpIn:
			if !ok || !contains(r.Values, v) {
				return false
			}
		case OpNotIn:
			if ok && contains(r.Values, v) {
				return false
			}
		}
	}
	return true
}

func contains(vs []string, v string) bool {
	for _, x := range vs {
		if x == v {
			return true
		}
	}
	return false
}

// Parse parses the selector syntax. The empty string yields the
// match-all selector.
func Parse(in string) (Selector, error) {
	s := Selector{str: strings.TrimSpace(in)}
	p := &parser{in: in}
	for {
		p.skipSpace()
		if p.eof() {
			break
		}
		req, err := p.requirement()
		if err != nil {
			return Selector{}, err
		}
		s.reqs = append(s.reqs, req)
		p.skipSpace()
		if p.eof() {
			break
		}
		if !p.eat(',') {
			return Selector{}, fmt.Errorf("selector: expected ',' at %q", p.rest())
		}
	}
	return s, nil
}

// MustParse panics on error (static selectors in code/tests).
func MustParse(in string) Selector {
	s, err := Parse(in)
	if err != nil {
		panic(err)
	}
	return s
}

type parser struct {
	in  string
	pos int
}

func (p *parser) eof() bool    { return p.pos >= len(p.in) }
func (p *parser) rest() string { return p.in[p.pos:] }
func (p *parser) peek() byte   { return p.in[p.pos] }
func (p *parser) skipSpace() {
	for !p.eof() && (p.peek() == ' ' || p.peek() == '\t') {
		p.pos++
	}
}

func (p *parser) eat(c byte) bool {
	if !p.eof() && p.peek() == c {
		p.pos++
		return true
	}
	return false
}

func isKeyChar(c byte) bool {
	return c == '-' || c == '_' || c == '.' || c == '/' ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func (p *parser) ident() string {
	start := p.pos
	for !p.eof() && isKeyChar(p.peek()) {
		p.pos++
	}
	return p.in[start:p.pos]
}

func (p *parser) requirement() (Requirement, error) {
	p.skipSpace()
	if p.eat('!') {
		key := p.ident()
		if key == "" {
			return Requirement{}, fmt.Errorf("selector: expected key after '!'")
		}
		return Requirement{Key: key, Op: OpNotExists}, nil
	}
	key := p.ident()
	if key == "" {
		return Requirement{}, fmt.Errorf("selector: expected key at %q", p.rest())
	}
	p.skipSpace()
	switch {
	case p.eof() || p.peek() == ',':
		// Bare key: "in"/"notin" without operator context are keys here.
		return Requirement{Key: key, Op: OpExists}, nil
	case p.eat('='):
		p.eat('=') // tolerate "=="
		return Requirement{Key: key, Op: OpEq, Values: []string{p.value()}}, nil
	case p.eat('!'):
		if !p.eat('=') {
			return Requirement{}, fmt.Errorf("selector: expected '!=' after %q", key)
		}
		return Requirement{Key: key, Op: OpNeq, Values: []string{p.value()}}, nil
	}
	word := p.ident()
	switch word {
	case "in", "notin":
		p.skipSpace()
		if !p.eat('(') {
			return Requirement{}, fmt.Errorf("selector: expected '(' after %q", word)
		}
		var vals []string
		for {
			p.skipSpace()
			vals = append(vals, p.value())
			p.skipSpace()
			if p.eat(')') {
				break
			}
			if !p.eat(',') {
				return Requirement{}, fmt.Errorf("selector: expected ',' or ')' in set at %q", p.rest())
			}
		}
		sort.Strings(vals)
		op := OpIn
		if word == "notin" {
			op = OpNotIn
		}
		return Requirement{Key: key, Op: op, Values: vals}, nil
	case "":
		return Requirement{}, fmt.Errorf("selector: unexpected char at %q", p.rest())
	default:
		return Requirement{}, fmt.Errorf("selector: unknown operator %q", word)
	}
}

// value reads until comma/closing paren (trimmed); values are unquoted.
func (p *parser) value() string {
	start := p.pos
	for !p.eof() && p.peek() != ',' && p.peek() != ')' {
		p.pos++
	}
	return strings.TrimSpace(p.in[start:p.pos])
}
