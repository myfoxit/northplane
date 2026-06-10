package bundle

import (
	"bytes"
	"strings"
	"testing"
)

const sample = `kind: Service
metadata:
  name: postgres
  host: db-01
spec:
  checkCommand: "agent:exec:check_pgsql"
---
kind: Host
metadata:
  name: db-01
  folder: /prod
  labels:
    env: prod
spec:
  address: 10.0.0.5
  checkCommand: builtin:icmp
---
kind: Channel
metadata:
  name: ops-mail
spec:
  type: email
  config:
    host: mail.internal
---
kind: Dashboard
metadata:
  name: wallboard
data:
  layout:
    cols: 12
`

func TestParseValidatesStructure(t *testing.T) {
	docs, err := Parse(strings.NewReader(sample))
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 4 {
		t.Fatalf("parsed %d docs", len(docs))
	}
	if errs := Validate(docs); len(errs) != 0 {
		t.Fatalf("valid bundle rejected: %v", errs)
	}

	cases := []struct {
		name, in, wantErr string
	}{
		{"unknown kind", "kind: Nonsense\nmetadata:\n  name: x\n", "unknown kind"},
		{"missing kind", "metadata:\n  name: x\nspec: {a: 1}\n", "missing kind"},
		{"missing name", "kind: Host\nspec: {a: 1}\n", "missing metadata.name"},
		{"broken yaml", "kind: Host\n\tmetadata: x\n", "document 1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseBytes([]byte(tc.in)); err == nil ||
				!strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want %q error, got %v", tc.wantErr, err)
			}
		})
	}

	// separator-only / empty documents are skipped, not errors
	docs, err = ParseBytes([]byte("---\n---\nkind: Host\nmetadata:\n  name: a\n---\n"))
	if err != nil || len(docs) != 1 {
		t.Fatalf("empty docs: %v / %d", err, len(docs))
	}
}

func TestRenderCanonicalOrderAndRoundTrip(t *testing.T) {
	docs, err := Parse(strings.NewReader(sample))
	if err != nil {
		t.Fatal(err)
	}
	out, err := Render(docs)
	if err != nil {
		t.Fatal(err)
	}
	// canonical order: Channel < Host < Service < Dashboard (KindOrder)
	idx := func(sub string) int { return bytes.Index(out, []byte(sub)) }
	if !(idx("ops-mail") < idx("db-01") && idx("db-01") < idx("postgres") &&
		idx("postgres") < idx("wallboard")) {
		t.Fatalf("apply order violated:\n%s", out)
	}

	// round-trip: parse(render) re-renders byte-identically (SPEC §11.6)
	docs2, err := ParseBytes(out)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	out2, err := Render(docs2)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, out2) {
		t.Fatalf("round-trip not stable:\n--- first ---\n%s\n--- second ---\n%s", out, out2)
	}

	// content survives: labels, folder, data payloads
	if !bytes.Contains(out, []byte("env: prod")) || !bytes.Contains(out, []byte("folder: /prod")) ||
		!bytes.Contains(out, []byte("cols: 12")) {
		t.Fatalf("content lost in round-trip:\n%s", out)
	}
}

func TestRenderSortsWithinKind(t *testing.T) {
	docs := []Doc{
		{Kind: "Host", Metadata: Metadata{Name: "zulu"}},
		{Kind: "Host", Metadata: Metadata{Name: "alpha"}},
		{Kind: "Service", Metadata: Metadata{Name: "b", Host: "zulu"}},
		{Kind: "Service", Metadata: Metadata{Name: "a", Host: "zulu"}},
		{Kind: "Service", Metadata: Metadata{Name: "z", Host: "alpha"}},
	}
	out, err := Render(docs)
	if err != nil {
		t.Fatal(err)
	}
	order := []string{"name: alpha", "name: zulu", "host: alpha", "name: a\n", "name: b\n"}
	last := -1
	for _, want := range order {
		i := bytes.Index(out, []byte(want))
		if i <= last {
			t.Fatalf("sort order broken at %q:\n%s", want, out)
		}
		last = i
	}
}

func TestValidateFindsProblems(t *testing.T) {
	docs := []Doc{
		{Kind: "Host", Metadata: Metadata{Name: "a"}},
		{Kind: "Host", Metadata: Metadata{Name: "a"}},                  // duplicate
		{Kind: "Service", Metadata: Metadata{Name: "svc"}},             // missing host
		{Kind: "Host", Metadata: Metadata{Name: "bad\tname"}},          // invalid name
		{Kind: "Service", Metadata: Metadata{Name: "ok", Host: "a"}},   // fine
		{Kind: "Service", Metadata: Metadata{Name: "ok", Host: "a"}},   // duplicate service ident
		{Kind: "Service", Metadata: Metadata{Name: "ok", Host: "b"}},   // distinct: other host
	}
	errs := Validate(docs)
	if len(errs) != 4 {
		t.Fatalf("want 4 problems, got %d: %v", len(errs), errs)
	}
	for _, want := range []string{"duplicate Host/a", "service requires metadata.host",
		"invalid name", "duplicate Service/a/ok"} {
		found := false
		for _, e := range errs {
			if strings.Contains(e, want) {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing problem %q in %v", want, errs)
		}
	}
}

func TestIdentAndKnownKind(t *testing.T) {
	if (Doc{Kind: "Host", Metadata: Metadata{Name: "x"}}).Ident() != "Host/x" {
		t.Fatal("host ident")
	}
	if (Doc{Kind: "Service", Metadata: Metadata{Name: "s", Host: "h"}}).Ident() != "Service/h/s" {
		t.Fatal("service ident")
	}
	if !KnownKind("Host") || KnownKind("host") || KnownKind("") {
		t.Fatal("KnownKind is case-sensitive over KindOrder")
	}
	for _, k := range KindOrder {
		if !KnownKind(k) {
			t.Fatalf("KindOrder entry %q not known", k)
		}
	}
}
