package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"
)

// TestTemplatesRender renders every embedded template and asserts the
// derived identifiers land where they should — a regression net against a
// template that stops compiling or loses a substitution.
func TestTemplatesRender(t *testing.T) {
	tpls, err := template.ParseFS(templatesFS, "templates/*.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	n := ParseName("contact-group")

	checks := map[string][]string{
		"model.go.tmpl":        {"package model", "type ContactGroup struct"},
		"storage_kind.go.tmpl": {"package storage", `KindContactGroup = "contact-group"`},
		"api.go.tmpl":          {"package api", "func (a *API) registerContactGroups()", `a.resourceCRUD("contact-groups", storage.KindContactGroup`},
		"types.ts.tmpl":        {"export interface ContactGroup"},
		"page.tsx.tmpl":        {"export function ContactGroupsPage()", `resourceApi<ContactGroup>('contact-groups')`},
	}
	for name, wants := range checks {
		out, err := render(tpls, name, n)
		if err != nil {
			t.Fatalf("render %s: %v", name, err)
		}
		for _, w := range wants {
			if !strings.Contains(string(out), w) {
				t.Errorf("%s: missing %q in:\n%s", name, w, out)
			}
		}
	}
}

// TestNewResourceOut runs the real command into a temp dir and checks the
// five files are produced with unique (collision-free) flattened names.
func TestNewResourceOut(t *testing.T) {
	out := t.TempDir()
	if err := newResource([]string{"Widget", "--out", out}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 5 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("expected 5 generated files, got %d: %v", len(entries), names)
	}
	// The Go API stub must reference the model + storage kind so it compiles
	// once wired.
	apiPath := filepath.Join(out, "internal_api__gen_widget.go")
	b, err := os.ReadFile(apiPath)
	if err != nil {
		t.Fatalf("api stub missing: %v", err)
	}
	if !strings.Contains(string(b), "model.Widget{}") {
		t.Errorf("api stub does not reference model.Widget:\n%s", b)
	}
}

// TestNewResourceRefusesOverwrite confirms the no-clobber guard (and that
// --force lifts it).
func TestNewResourceRefusesOverwrite(t *testing.T) {
	out := t.TempDir()
	if err := newResource([]string{"Widget", "--out", out}); err != nil {
		t.Fatal(err)
	}
	if err := newResource([]string{"Widget", "--out", out}); err == nil {
		t.Fatal("expected refusal to overwrite without --force")
	}
	if err := newResource([]string{"Widget", "--out", out, "--force"}); err != nil {
		t.Fatalf("--force should overwrite: %v", err)
	}
}

// TestNewResourceNeedsName guards the arg parsing.
func TestNewResourceNeedsName(t *testing.T) {
	if err := newResource([]string{"--dry-run"}); err == nil {
		t.Fatal("expected error when no name is given")
	}
	if err := newResource([]string{"Widget", "--bogus"}); err == nil {
		t.Fatal("expected error on unknown flag")
	}
}
