package api

// Coverage for /api/v1/users/{id|me}/preferences (P1 parity: UI settings
// are API-settable). Self-access needs no permission beyond auth; foreign
// access needs admin:users; values are validated.

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/northplane/northplane/internal/auth"
	"github.com/northplane/northplane/internal/model"
)

func TestPreferencesSelfRoundTrip(t *testing.T) {
	u := bootUserAPI(t)

	// unset → empty defaults
	code, body := u.req("GET", "/api/v1/users/me/preferences", nil)
	if code != http.StatusOK {
		t.Fatalf("get: %d %s", code, body)
	}
	var prefs model.Preferences
	if err := json.Unmarshal(body, &prefs); err != nil || prefs.RefreshIntervalMs != nil {
		t.Fatalf("want empty prefs, got %s (err %v)", body, err)
	}

	// set and read back
	code, body = u.req("PUT", "/api/v1/users/me/preferences",
		map[string]any{"refreshIntervalMs": 10000, "extra": map[string]string{"theme": "dark"}})
	if code != http.StatusOK {
		t.Fatalf("put: %d %s", code, body)
	}
	code, body = u.req("GET", "/api/v1/users/me/preferences", nil)
	if code != http.StatusOK {
		t.Fatalf("get2: %d %s", code, body)
	}
	if err := json.Unmarshal(body, &prefs); err != nil {
		t.Fatal(err)
	}
	if prefs.RefreshIntervalMs == nil || *prefs.RefreshIntervalMs != 10000 || prefs.Extra["theme"] != "dark" {
		t.Fatalf("round-trip mismatch: %s", body)
	}

	// 0 = off is valid
	if code, body = u.req("PUT", "/api/v1/users/me/preferences",
		map[string]any{"refreshIntervalMs": 0}); code != http.StatusOK {
		t.Fatalf("put off: %d %s", code, body)
	}

	// out-of-range cadence rejected
	if code, body = u.req("PUT", "/api/v1/users/me/preferences",
		map[string]any{"refreshIntervalMs": 50}); code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422 for 50ms, got %d %s", code, body)
	}
}

func TestPreferencesForeignNeedsAdmin(t *testing.T) {
	u := bootUserAPI(t)

	// a second principal without admin:users
	clear, tok := auth.MintToken(model.DefaultTenant, "plain",
		[]model.Permission{"objects:read"}, nil)
	if err := u.store.CreateAPIToken(u.t.Context(), tok); err != nil {
		t.Fatal(err)
	}
	asPlain := func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+clear) }

	// plain principal can manage its own…
	code, body := u.do("PUT", "/api/v1/users/me/preferences",
		map[string]any{"refreshIntervalMs": 5000}, asPlain)
	if code != http.StatusOK {
		t.Fatalf("self put: %d %s", code, body)
	}
	// …but not someone else's
	code, body = u.do("GET", "/api/v1/users/"+tokIDOther(u)+"/preferences", nil, asPlain)
	if code != http.StatusForbidden {
		t.Fatalf("foreign get without admin: want 403, got %d %s", code, body)
	}

	// the admin token can read/write the plain principal's preferences
	code, body = u.req("PUT", "/api/v1/users/"+tok.ID+"/preferences",
		map[string]any{"refreshIntervalMs": 30000})
	if code != http.StatusOK {
		t.Fatalf("admin foreign put: %d %s", code, body)
	}
	code, body = u.do("GET", "/api/v1/users/me/preferences", nil, asPlain)
	if code != http.StatusOK {
		t.Fatalf("self get: %d %s", code, body)
	}
	var prefs model.Preferences
	_ = json.Unmarshal(body, &prefs)
	if prefs.RefreshIntervalMs == nil || *prefs.RefreshIntervalMs != 30000 {
		t.Fatalf("admin write not visible to owner: %s", body)
	}

	// unauthenticated → 401
	if code, _ = u.do("GET", "/api/v1/users/me/preferences", nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", code)
	}
}

// tokIDOther returns some actor id that is not the caller's (the admin
// token's id serves fine — it exists and belongs to someone else).
func tokIDOther(u *userAPI) string {
	toks, err := u.store.ListAPITokens(u.t.Context(), model.DefaultTenant)
	if err != nil || len(toks) == 0 {
		u.t.Fatalf("list tokens: %v", err)
	}
	for _, tk := range toks {
		if tk.Name == "test-admin" {
			return tk.ID
		}
	}
	u.t.Fatal("admin token not found")
	return ""
}
