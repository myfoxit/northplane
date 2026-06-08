package auth

// Authorisation-model coverage (SPEC §11.2): the permission-implication
// matrix exercised through Principal.Allow (which delegates to
// model.Permission.Implies) and the folder-subtree scoping in
// Principal.AllowFolder — including the sibling-prefix false-positive
// guard (/foo must not grant /foobar).

import (
	"context"
	"testing"

	"github.com/northplane/northplane/internal/model"
)

func TestPrincipalAllowMatrix(t *testing.T) {
	cases := []struct {
		name string
		have []model.Permission
		want model.Permission
		ok   bool
	}{
		// exact matches
		{"exact-match", []model.Permission{"objects:read"}, "objects:read", true},
		{"exact-mismatch-action", []model.Permission{"objects:read"}, "objects:write", false},
		{"exact-mismatch-resource", []model.Permission{"objects:read"}, "alerts:read", false},

		// global wildcards
		{"star-star-grants-all", []model.Permission{"*:*"}, "anything:goes", true},
		{"bare-star-grants-all", []model.Permission{"*"}, "alerts:ack", true},

		// resource wildcard
		{"resource-wildcard-hit", []model.Permission{"admin:*"}, "admin:users", true},
		{"resource-wildcard-other-action", []model.Permission{"admin:*"}, "admin:ai", true},
		{"resource-wildcard-wrong-resource", []model.Permission{"admin:*"}, "objects:read", false},

		// action wildcard (left side is the wildcard, action="*")
		{"action-wildcard-hit", []model.Permission{"*:read"}, "objects:read", true},
		{"action-wildcard-miss", []model.Permission{"*:read"}, "objects:write", false},

		// no permissions at all
		{"empty-perms-denied", nil, "objects:read", false},

		// multiple perms, one matches
		{"first-of-many", []model.Permission{"alerts:read", "objects:read"}, "objects:read", true},
		{"none-of-many", []model.Permission{"alerts:read", "events:read"}, "objects:write", false},

		// malformed wanted/held perms (no colon) — Implies returns false
		// unless it's an exact/global-wildcard match.
		{"malformed-have-no-colon", []model.Permission{"objectsread"}, "objects:read", false},
		{"malformed-want-no-colon", []model.Permission{"objects:read"}, "objectsread", false},
		{"malformed-both-equal", []model.Permission{"weird"}, "weird", true}, // exact-string equality still holds
		{"malformed-both-differ", []model.Permission{"weird"}, "other", false},

		// real built-in admin role grants everything
		{"admin-role-perm", []model.Permission{"*:*"}, "config:write", true},
		// real viewer-style perm does not grant a write
		{"viewer-cannot-write", []model.Permission{"objects:read", "alerts:read"}, "objects:write", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &Principal{Perms: tc.have}
			if got := p.Allow(tc.want); got != tc.ok {
				t.Fatalf("Allow(%q) with %v = %v, want %v", tc.want, tc.have, got, tc.ok)
			}
		})
	}
}

func TestPermissionImpliesDirect(t *testing.T) {
	// A few asymmetry checks straight against Implies: a narrow grant must
	// never imply a broader request.
	cases := []struct {
		have, want model.Permission
		ok         bool
	}{
		{"objects:read", "*:*", false}, // narrow can't grant global
		{"admin:users", "admin:*", false},
		{"*:*", "admin:users", true},
		{"admin:*", "admin:*", true},
		{"objects:*", "objects:read", true},
	}
	for _, tc := range cases {
		if got := tc.have.Implies(tc.want); got != tc.ok {
			t.Errorf("%q.Implies(%q) = %v, want %v", tc.have, tc.want, got, tc.ok)
		}
	}
}

func TestAllowFolder(t *testing.T) {
	cases := []struct {
		name    string
		scope   string // Principal.Folder
		folder  string // folder being checked
		allowed bool
	}{
		// unscoped principals see everything
		{"empty-scope-allows-all", "", "/prod/wien", true},
		{"root-scope-allows-all", "/", "/prod/wien", true},
		{"root-scope-allows-root", "/", "/", true},

		// exact subtree root
		{"exact-match", "/prod", "/prod", true},
		// A scope written with a trailing slash ("/prod/") is treated as a
		// distinct string: it matches descendants ("/prod/x") but not the
		// bare root ("/prod"), since TrimSuffix collapses it back to "/prod/".
		{"trailing-slash-scope-matches-child", "/prod/", "/prod/wien", true},
		{"trailing-slash-scope-not-bare-root", "/prod/", "/prod", false},

		// genuine descendants
		{"direct-child", "/prod", "/prod/wien", true},
		{"deep-descendant", "/prod", "/prod/wien/db/01", true},

		// out of scope
		{"unrelated-tree", "/prod", "/staging", false},
		{"parent-not-in-child-scope", "/prod/wien", "/prod", false},

		// the critical sibling-prefix false-positive guard
		{"sibling-prefix-foobar", "/foo", "/foobar", false},
		{"sibling-prefix-prod", "/prod", "/production", false},

		// empty folder normalises to root, only allowed by all-scopes
		{"empty-folder-scoped-out", "/prod", "", false},

		// scoped principal asked about root → denied
		{"root-folder-under-scope", "/prod", "/", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &Principal{Folder: tc.scope}
			if got := p.AllowFolder(tc.folder); got != tc.allowed {
				t.Fatalf("Folder=%q AllowFolder(%q) = %v, want %v",
					tc.scope, tc.folder, got, tc.allowed)
			}
		})
	}
}

func TestWithPrincipalAndFrom(t *testing.T) {
	t.Run("round-trip", func(t *testing.T) {
		p := &Principal{ActorID: "u1", Name: "Ada", ActorType: model.ActorUser}
		ctx := WithPrincipal(context.Background(), p)
		got := From(ctx)
		if got == nil || got.ActorID != "u1" {
			t.Fatalf("From returned %+v", got)
		}
	})

	t.Run("anonymous-context", func(t *testing.T) {
		if got := From(context.Background()); got != nil {
			t.Fatalf("From on bare context = %+v, want nil", got)
		}
	})
}
