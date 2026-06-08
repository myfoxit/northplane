package auth

// Token/session authentication coverage (SPEC §11.2). Tests drive a real
// SQLite store (t.TempDir, like storage_test.go / admin_users_test.go)
// since Authenticate resolves credentials against persisted tokens,
// sessions and users. Covers: mint→authenticate round-trip, scope/role
// expansion, AIAgent actor flag, expiry, IP-bind allow/deny, disabled
// users, unknown/malformed tokens, and the session-cookie path.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
)

func testAuth(t *testing.T) (*Authenticator, *storage.Store) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	store, err := storage.Open(ctx, storage.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("storage open: %v", err)
	}
	t.Cleanup(func() { cancel(); store.Close() })
	return &Authenticator{Store: store}, store
}

// mintReq builds an HTTP request bearing the given clear token.
func bearerReq(clear string) *http.Request {
	r := httptest.NewRequest("GET", "/api/v1/objects", nil)
	r.Header.Set("Authorization", "Bearer "+clear)
	return r
}

func TestMintTokenShape(t *testing.T) {
	scopes := []model.Permission{"objects:read", "alerts:ack"}

	t.Run("plain", func(t *testing.T) {
		clear, tok := MintToken(model.DefaultTenant, "ci", scopes, nil)
		if len(clear) <= len("np_") || clear[:3] != "np_" {
			t.Fatalf("clear token has no np_ prefix: %q", clear)
		}
		if tok.Prefix == "" || len(tok.Prefix) != tokenPrefixLen {
			t.Fatalf("prefix wrong: %q", tok.Prefix)
		}
		// The clear body (after np_) must start with the stored prefix.
		body := clear[len("np_"):]
		if body[:tokenPrefixLen] != tok.Prefix {
			t.Fatalf("prefix %q is not the head of body %q", tok.Prefix, body)
		}
		if tok.Hash == "" || tok.Hash == body {
			t.Fatal("hash must be a non-cleartext argon2 hash")
		}
		if !VerifySecret(body, tok.Hash) {
			t.Fatal("minted hash does not verify against its own body")
		}
		if len(tok.Scopes) != 2 {
			t.Fatalf("scopes not carried: %v", tok.Scopes)
		}
		if tok.AIAgent {
			t.Fatal("AIAgent should default false")
		}
	})

	t.Run("opts-carry-aiagent-and-roles-and-ipbind", func(t *testing.T) {
		exp := time.Now().Add(time.Hour)
		_, tok := MintToken(model.DefaultTenant, "agent", nil, &model.APIToken{
			AIAgent:   true,
			RoleNames: []string{"viewer"},
			IPBind:    []string{"10.0.0.0/8"},
			ExpiresAt: &exp,
			CreatedBy: "admin-1",
		})
		if !tok.AIAgent {
			t.Fatal("AIAgent flag not carried from opts")
		}
		if len(tok.RoleNames) != 1 || tok.RoleNames[0] != "viewer" {
			t.Fatalf("role names not carried: %v", tok.RoleNames)
		}
		if len(tok.IPBind) != 1 {
			t.Fatalf("ip bind not carried: %v", tok.IPBind)
		}
		if tok.ExpiresAt == nil || tok.CreatedBy != "admin-1" {
			t.Fatalf("expiry/createdBy not carried: %+v", tok)
		}
	})
}

func TestAuthenticateTokenRoundTrip(t *testing.T) {
	a, store := testAuth(t)
	ctx := context.Background()

	clear, tok := MintToken(model.DefaultTenant, "round-trip",
		[]model.Permission{"objects:read"}, nil)
	if err := store.CreateAPIToken(ctx, tok); err != nil {
		t.Fatal(err)
	}

	p, err := a.Authenticate(bearerReq(clear))
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if p == nil {
		t.Fatal("nil principal for a valid token")
	}
	if p.ActorType != model.ActorToken {
		t.Fatalf("actor type = %q, want token", p.ActorType)
	}
	if p.TokenID != tok.ID || p.TenantID != model.DefaultTenant {
		t.Fatalf("principal identity wrong: %+v", p)
	}
	if !p.Allow("objects:read") {
		t.Fatal("scope objects:read not granted")
	}
	if p.Allow("objects:write") {
		t.Fatal("unscoped permission wrongly granted")
	}
}

func TestAuthenticateAIAgentActor(t *testing.T) {
	a, store := testAuth(t)
	ctx := context.Background()

	clear, tok := MintToken(model.DefaultTenant, "ai", []model.Permission{"alerts:read"},
		&model.APIToken{AIAgent: true})
	if err := store.CreateAPIToken(ctx, tok); err != nil {
		t.Fatal(err)
	}
	p, err := a.Authenticate(bearerReq(clear))
	if err != nil || p == nil {
		t.Fatalf("authenticate ai: %v", err)
	}
	if p.ActorType != model.ActorAI {
		t.Fatalf("AIAgent token must map to ActorAI, got %q", p.ActorType)
	}
}

func TestAuthenticateTokenExpandsRolePerms(t *testing.T) {
	a, store := testAuth(t)
	ctx := context.Background()

	// "viewer" is a built-in role seeded at store open; it grants
	// objects:read but not objects:write.
	clear, tok := MintToken(model.DefaultTenant, "role-bound", nil,
		&model.APIToken{RoleNames: []string{"viewer"}})
	if err := store.CreateAPIToken(ctx, tok); err != nil {
		t.Fatal(err)
	}
	p, err := a.Authenticate(bearerReq(clear))
	if err != nil || p == nil {
		t.Fatalf("authenticate: %v", err)
	}
	if !p.Allow("objects:read") {
		t.Fatal("viewer role should grant objects:read")
	}
	if p.Allow("objects:write") {
		t.Fatal("viewer role must not grant objects:write")
	}
}

func TestAuthenticateTokenFailures(t *testing.T) {
	a, store := testAuth(t)
	ctx := context.Background()

	t.Run("unknown-token", func(t *testing.T) {
		// well-formed but never persisted
		clear, _ := MintToken(model.DefaultTenant, "ghost", nil, nil)
		p, err := a.Authenticate(bearerReq(clear))
		if err == nil || p != nil {
			t.Fatalf("unknown token must fail: p=%+v err=%v", p, err)
		}
	})

	t.Run("malformed-too-short", func(t *testing.T) {
		// body shorter than the prefix length
		p, err := a.Authenticate(bearerReq("np_short"))
		if err == nil || p != nil {
			t.Fatalf("short token must error: p=%+v err=%v", p, err)
		}
	})

	t.Run("expired-token", func(t *testing.T) {
		past := time.Now().Add(-time.Hour)
		clear, tok := MintToken(model.DefaultTenant, "stale",
			[]model.Permission{"objects:read"}, &model.APIToken{ExpiresAt: &past})
		if err := store.CreateAPIToken(ctx, tok); err != nil {
			t.Fatal(err)
		}
		p, err := a.Authenticate(bearerReq(clear))
		if err == nil || p != nil {
			t.Fatalf("expired token must fail: p=%+v err=%v", p, err)
		}
	})

	t.Run("right-prefix-wrong-secret", func(t *testing.T) {
		// Persist a real token, then present a body that shares the prefix
		// but differs in the tail — the argon2 verify must reject it and
		// the candidate loop must fall through to "invalid token".
		clear, tok := MintToken(model.DefaultTenant, "collision",
			[]model.Permission{"objects:read"}, nil)
		if err := store.CreateAPIToken(ctx, tok); err != nil {
			t.Fatal(err)
		}
		body := clear[len("np_"):]
		// keep prefix, replace the rest with a different (same-length) body
		forged := "np_" + body[:tokenPrefixLen] + "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
		p, err := a.Authenticate(bearerReq(forged))
		if err == nil || p != nil {
			t.Fatalf("forged tail must fail: p=%+v err=%v", p, err)
		}
	})
}

func TestAuthenticateTokenIPBind(t *testing.T) {
	a, store := testAuth(t)
	ctx := context.Background()

	clear, tok := MintToken(model.DefaultTenant, "ip-bound",
		[]model.Permission{"objects:read"},
		&model.APIToken{IPBind: []string{"10.0.0.0/8", "192.168.1.5"}})
	if err := store.CreateAPIToken(ctx, tok); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		remote string
		ok     bool
	}{
		{"in-cidr", "10.20.30.40:5555", true},
		{"exact-ip", "192.168.1.5:1234", true},
		{"out-of-cidr", "172.16.0.1:1234", false},
		{"sibling-ip", "192.168.1.6:1234", false},
		{"unparseable-remote", "garbage", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := bearerReq(clear)
			r.RemoteAddr = tc.remote
			p, err := a.Authenticate(r)
			if tc.ok {
				if err != nil || p == nil {
					t.Fatalf("expected allow from %q: p=%+v err=%v", tc.remote, p, err)
				}
			} else {
				if err == nil || p != nil {
					t.Fatalf("expected deny from %q: p=%+v err=%v", tc.remote, p, err)
				}
			}
		})
	}
	_ = ctx
}

func TestAuthenticateNoCredentialsIsAnonymous(t *testing.T) {
	a, _ := testAuth(t)
	r := httptest.NewRequest("GET", "/api/v1/objects", nil)
	p, err := a.Authenticate(r)
	if err != nil || p != nil {
		t.Fatalf("no creds must be anonymous (nil,nil): p=%+v err=%v", p, err)
	}
}

func TestAuthenticateSessionPath(t *testing.T) {
	a, store := testAuth(t)
	ctx := context.Background()

	user, err := store.CreateUser(ctx, &model.User{
		Name: "Sandra", Email: "sandra@example.net", Local: true,
		PassHash: HashSecret("korrekt-pferd-batterie"), Roles: []string{"viewer"},
	})
	if err != nil {
		t.Fatal(err)
	}

	sess, err := a.NewSession(ctx, user.ID, model.DefaultTenant,
		[]string{"viewer"}, nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	withCookie := func(value string) *http.Request {
		r := httptest.NewRequest("GET", "/api/v1/objects", nil)
		r.AddCookie(&http.Cookie{Name: "np_session", Value: value})
		return r
	}

	t.Run("valid-session", func(t *testing.T) {
		p, err := a.Authenticate(withCookie(sess))
		if err != nil || p == nil {
			t.Fatalf("valid session: p=%+v err=%v", p, err)
		}
		if p.ActorType != model.ActorUser || p.ActorID != user.ID {
			t.Fatalf("session principal wrong: %+v", p)
		}
		if p.SessionID != sess {
			t.Fatalf("session id not carried: %q", p.SessionID)
		}
		if !p.Allow("objects:read") {
			t.Fatal("session viewer role should grant objects:read")
		}
	})

	t.Run("unknown-session", func(t *testing.T) {
		p, err := a.Authenticate(withCookie("does-not-exist"))
		if err == nil || p != nil {
			t.Fatalf("unknown session must fail: p=%+v err=%v", p, err)
		}
	})

	t.Run("disabled-user", func(t *testing.T) {
		// disable the user, then the existing session must be rejected.
		u, _ := store.GetUser(ctx, user.ID)
		u.Disabled = true
		if err := store.UpdateUser(ctx, u); err != nil {
			t.Fatalf("disable user: %v", err)
		}
		p, err := a.Authenticate(withCookie(sess))
		if err == nil || p != nil {
			t.Fatalf("disabled user session must fail: p=%+v err=%v", p, err)
		}
	})
}

func TestAuthenticateExpiredSession(t *testing.T) {
	a, store := testAuth(t)
	ctx := context.Background()

	user, err := store.CreateUser(ctx, &model.User{
		Name: "Ephemeral", Email: "eph@example.net", Local: true,
		PassHash: HashSecret("korrekt-pferd-batterie"),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Negative TTL → already expired at creation (no sleep needed).
	sess, err := a.NewSession(ctx, user.ID, model.DefaultTenant, nil, nil, -time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: "np_session", Value: sess})
	p, err := a.Authenticate(r)
	if err == nil || p != nil {
		t.Fatalf("expired session must fail: p=%+v err=%v", p, err)
	}
}

func TestAuthenticateTokenBareString(t *testing.T) {
	a, store := testAuth(t)
	ctx := context.Background()

	clear, tok := MintToken(model.DefaultTenant, "cli",
		[]model.Permission{"objects:read"}, &model.APIToken{AIAgent: true})
	if err := store.CreateAPIToken(ctx, tok); err != nil {
		t.Fatal(err)
	}

	t.Run("valid-with-prefix", func(t *testing.T) {
		p, err := AuthenticateToken(ctx, a, clear)
		if err != nil || p == nil {
			t.Fatalf("bare auth: p=%+v err=%v", p, err)
		}
		if p.ActorType != model.ActorAI || p.TokenID != tok.ID {
			t.Fatalf("bare principal wrong: %+v", p)
		}
	})

	t.Run("valid-without-np-prefix", func(t *testing.T) {
		// AuthenticateToken trims an optional np_ prefix; the bare body works too.
		p, err := AuthenticateToken(ctx, a, clear[len("np_"):])
		if err != nil || p == nil {
			t.Fatalf("bare body auth: p=%+v err=%v", p, err)
		}
	})

	t.Run("expired", func(t *testing.T) {
		past := time.Now().Add(-time.Hour)
		c, tk := MintToken(model.DefaultTenant, "old", nil, &model.APIToken{ExpiresAt: &past})
		if err := store.CreateAPIToken(ctx, tk); err != nil {
			t.Fatal(err)
		}
		if p, err := AuthenticateToken(ctx, a, c); err == nil || p != nil {
			t.Fatalf("expired bare token must fail: p=%+v err=%v", p, err)
		}
	})

	t.Run("malformed", func(t *testing.T) {
		if p, err := AuthenticateToken(ctx, a, "np_x"); err == nil || p != nil {
			t.Fatalf("short bare token must fail: p=%+v err=%v", p, err)
		}
	})

	t.Run("unknown", func(t *testing.T) {
		c, _ := MintToken(model.DefaultTenant, "ghost", nil, nil)
		if p, err := AuthenticateToken(ctx, a, c); err == nil || p != nil {
			t.Fatalf("unknown bare token must fail: p=%+v err=%v", p, err)
		}
	})
}

func TestNewSessionPersists(t *testing.T) {
	a, store := testAuth(t)
	ctx := context.Background()
	id, err := a.NewSession(ctx, "user-x", model.DefaultTenant,
		[]string{"viewer"}, []string{"grp"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("empty session id")
	}
	uid, tid, data, err := store.GetSession(ctx, id)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if uid != "user-x" || tid != model.DefaultTenant {
		t.Fatalf("session row wrong: uid=%q tid=%q", uid, tid)
	}
	if len(data.Roles) != 1 || data.Roles[0] != "viewer" || len(data.Groups) != 1 {
		t.Fatalf("session data wrong: %+v", data)
	}
}

func TestSecretsResolver(t *testing.T) {
	a, store := testAuth(t)
	ctx := context.Background()
	box := newTestBox(t)

	blob, err := box.Seal("s3cr3t-value")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutSecret(ctx, model.DefaultTenant, "db-pass", blob, "tester"); err != nil {
		t.Fatal(err)
	}

	resolve := SecretsResolver(store, box)

	t.Run("found", func(t *testing.T) {
		v, ok := resolve(model.DefaultTenant, "db-pass")
		if !ok || v != "s3cr3t-value" {
			t.Fatalf("resolve: v=%q ok=%v", v, ok)
		}
	})

	t.Run("missing-name", func(t *testing.T) {
		if v, ok := resolve(model.DefaultTenant, "nope"); ok || v != "" {
			t.Fatalf("missing secret: v=%q ok=%v", v, ok)
		}
	})

	t.Run("nil-box", func(t *testing.T) {
		nilResolve := SecretsResolver(store, nil)
		if v, ok := nilResolve(model.DefaultTenant, "db-pass"); ok || v != "" {
			t.Fatalf("nil box must return false: v=%q ok=%v", v, ok)
		}
	})

	t.Run("wrong-key-cannot-decrypt", func(t *testing.T) {
		otherBox := newTestBox(t)
		bad := SecretsResolver(store, otherBox)
		if v, ok := bad(model.DefaultTenant, "db-pass"); ok || v != "" {
			t.Fatalf("wrong key must fail to open: v=%q ok=%v", v, ok)
		}
	})
	_ = a
}

func TestRequestIsHTTPS(t *testing.T) {
	cases := []struct {
		name       string
		tls        bool
		fwdProto   string
		trustProxy bool
		want       bool
	}{
		{"direct-tls", true, "", false, true},
		{"plaintext-no-proxy", false, "", false, false},
		{"forwarded-https-trusted", false, "https", true, true},
		{"forwarded-https-untrusted", false, "https", false, false},
		{"forwarded-http-trusted", false, "http", true, false},
		{"forwarded-list-takes-first", false, "https, http", true, true},
		{"forwarded-list-first-http", false, "http, https", true, false},
		{"forwarded-case-insensitive", false, "HTTPS", true, true},
		{"forwarded-whitespace", false, "  https  ", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			if tc.tls {
				// httptest marks TLS when the URL scheme is https.
				r = httptest.NewRequest("GET", "https://example.net/", nil)
			}
			if tc.fwdProto != "" {
				r.Header.Set("X-Forwarded-Proto", tc.fwdProto)
			}
			if got := RequestIsHTTPS(r, tc.trustProxy); got != tc.want {
				t.Fatalf("RequestIsHTTPS = %v, want %v", got, tc.want)
			}
		})
	}
}
