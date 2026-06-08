package auth

// OIDC coverage limited to the pure, IdP-independent logic (SPEC §11.2):
// the claim helpers (stringSlice / orDefault), PKCE-state randomness
// (randB64), and the group→role mapping (OIDC.mapGroups) which resolves
// against persisted roles' IdPGroups. The networked surface — NewOIDC
// discovery, Start, Callback token exchange, LogoutURL — needs a live
// provider and is deliberately not unit-tested here (see report).

import (
	"context"
	"testing"

	"github.com/northplane/northplane/internal/config"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
)

func TestStringSlice(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want []string
	}{
		{"nil", nil, nil},
		{"wrong-type", "not-a-slice", nil},
		{"empty-slice", []any{}, nil},
		{"strings", []any{"a", "b"}, []string{"a", "b"}},
		{"mixed-drops-non-strings", []any{"a", 1, true, "b"}, []string{"a", "b"}},
		{"all-non-strings", []any{1, 2.0, false}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stringSlice(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("stringSlice(%v) = %v, want %v", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("idx %d: got %q want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestOrDefault(t *testing.T) {
	cases := []struct {
		s, def, want string
	}{
		{"", "fallback", "fallback"},
		{"value", "fallback", "value"},
		{"", "", ""},
	}
	for _, tc := range cases {
		if got := orDefault(tc.s, tc.def); got != tc.want {
			t.Errorf("orDefault(%q,%q) = %q, want %q", tc.s, tc.def, got, tc.want)
		}
	}
}

func TestRandB64(t *testing.T) {
	a := randB64(24)
	b := randB64(24)
	if a == "" || b == "" {
		t.Fatal("randB64 produced empty string")
	}
	if a == b {
		t.Fatal("two randB64 calls collided (not random)")
	}
	// RawURLEncoding of 24 bytes is 32 chars and URL-safe (no +,/,=).
	if len(a) != 32 {
		t.Fatalf("randB64(24) length = %d, want 32", len(a))
	}
}

func TestMapGroups(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	store, err := storage.Open(ctx, storage.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("storage open: %v", err)
	}
	t.Cleanup(func() { cancel(); store.Close() })

	// Persist two roles that map IdP groups onto role names.
	netops := model.Role{
		TenantID: model.DefaultTenant, Name: "netops",
		Permissions: []model.Permission{"objects:read"},
		IdPGroups:   []string{"network-admins", "noc"},
	}
	dba := model.Role{
		TenantID: model.DefaultTenant, Name: "dba",
		Permissions: []model.Permission{"objects:write"},
		IdPGroups:   []string{"db-team"},
	}
	for _, r := range []model.Role{netops, dba} {
		if _, err := store.PutResource(ctx, model.DefaultTenant,
			storage.KindRole, r.Name, r, -1); err != nil {
			t.Fatalf("put role %s: %v", r.Name, err)
		}
	}

	o := &OIDC{store: store}

	cases := []struct {
		name   string
		groups []string
		want   map[string]bool // role names expected (order-independent)
	}{
		{"no-groups", nil, map[string]bool{}},
		{"single-match", []string{"noc"}, map[string]bool{"netops": true}},
		{"db-match", []string{"db-team"}, map[string]bool{"dba": true}},
		{"multi-match", []string{"network-admins", "db-team"}, map[string]bool{"netops": true, "dba": true}},
		{"unknown-group", []string{"random-group"}, map[string]bool{}},
		{"alias-group-same-role", []string{"network-admins", "noc"}, map[string]bool{"netops": true}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := o.mapGroups(ctx, tc.groups)
			gotSet := map[string]bool{}
			for _, r := range got {
				gotSet[r] = true
			}
			if len(gotSet) != len(tc.want) {
				t.Fatalf("mapGroups(%v) = %v, want %v", tc.groups, got, keysOf(tc.want))
			}
			for r := range tc.want {
				if !gotSet[r] {
					t.Fatalf("mapGroups(%v) missing role %q (got %v)", tc.groups, r, got)
				}
			}
		})
	}
}

// keysOf is a tiny helper for clearer failure messages.
func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestNewOIDCUnconfigured confirms the one branch of NewOIDC reachable
// without network: an empty issuer yields (nil, nil) — OIDC disabled.
func TestNewOIDCUnconfigured(t *testing.T) {
	o, err := NewOIDC(context.Background(), config.OIDCConfig{}, "https://np.example",
		nil, nil, false)
	if err != nil {
		t.Fatalf("unconfigured NewOIDC error: %v", err)
	}
	if o != nil {
		t.Fatalf("unconfigured NewOIDC should return nil, got %+v", o)
	}
}
