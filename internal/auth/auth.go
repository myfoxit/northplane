package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
)

// RequestIsHTTPS reports whether the request reached the client over TLS,
// either directly (r.TLS) or via a trusted TLS-terminating reverse proxy
// that set X-Forwarded-Proto: https. Behind such a proxy the local
// listener is plaintext, so r.TLS alone would wrongly drop the Secure
// cookie flag and HSTS. Only honour the header when trustProxy is set.
func RequestIsHTTPS(r *http.Request, trustProxy bool) bool {
	if r.TLS != nil {
		return true
	}
	if trustProxy {
		proto := r.Header.Get("X-Forwarded-Proto")
		if i := strings.IndexByte(proto, ','); i >= 0 {
			proto = proto[:i]
		}
		return strings.EqualFold(strings.TrimSpace(proto), "https")
	}
	return false
}

// Principal is the authenticated actor attached to each request.
type Principal struct {
	ActorType model.ActorType
	ActorID   string
	Name      string
	TenantID  string
	Perms     []model.Permission
	Folder    string // scope subtree ("" or "/" = all)
	TokenID   string
	SessionID string
}

// Allow reports whether the principal holds a permission.
func (p *Principal) Allow(perm model.Permission) bool {
	for _, have := range p.Perms {
		if have.Implies(perm) {
			return true
		}
	}
	return false
}

// AllowFolder checks the folder scope (SPEC §11.2: roles bind to a
// folder subtree).
func (p *Principal) AllowFolder(folder string) bool {
	if p.Folder == "" || p.Folder == "/" {
		return true
	}
	f := folder
	if f == "" {
		f = "/"
	}
	return f == p.Folder || strings.HasPrefix(f, strings.TrimSuffix(p.Folder, "/")+"/")
}

type ctxKey int

const principalKey ctxKey = 1

// WithPrincipal attaches the principal to a context.
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, principalKey, p)
}

// From extracts the principal (nil when unauthenticated).
func From(ctx context.Context) *Principal {
	p, _ := ctx.Value(principalKey).(*Principal)
	return p
}

// --- API tokens (SPEC §11.2): np_<prefix8><secret>, argon2id hash ---

const tokenPrefixLen = 8

// argon2id parameters (interactive profile).
const (
	argonTime    = 1
	argonMemory  = 64 * 1024
	argonThreads = 4
	argonKeyLen  = 32
)

// HashSecret derives the storable hash: salt$hash (both hex).
func HashSecret(secret string) string {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		panic(err)
	}
	sum := argon2.IDKey([]byte(secret), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return hex.EncodeToString(salt) + "$" + hex.EncodeToString(sum)
}

// VerifySecret checks a presented secret against the stored hash.
func VerifySecret(secret, stored string) bool {
	saltHex, hashHex, ok := strings.Cut(stored, "$")
	if !ok {
		return false
	}
	salt, err1 := hex.DecodeString(saltHex)
	want, err2 := hex.DecodeString(hashHex)
	if err1 != nil || err2 != nil {
		return false
	}
	got := argon2.IDKey([]byte(secret), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return subtle.ConstantTimeCompare(got, want) == 1
}

// MintToken creates a new API token; returns the cleartext (shown once)
// and the storable row.
func MintToken(tenantID, name string, scopes []model.Permission, opts *model.APIToken) (string, *model.APIToken) {
	body := model.NewSecret(24)
	clear := "np_" + body
	t := &model.APIToken{TenantID: tenantID, Name: name,
		Prefix: body[:tokenPrefixLen], Hash: HashSecret(body), Scopes: scopes}
	if opts != nil {
		t.RoleNames, t.IPBind, t.AIAgent, t.ExpiresAt, t.CreatedBy =
			opts.RoleNames, opts.IPBind, opts.AIAgent, opts.ExpiresAt, opts.CreatedBy
	}
	return clear, t
}

// Authenticator resolves request credentials to principals.
type Authenticator struct {
	Store *storage.Store
}

// Authenticate inspects Authorization: Bearer np_… and session cookies.
func (a *Authenticator) Authenticate(r *http.Request) (*Principal, error) {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer np_") {
		return a.token(r, strings.TrimPrefix(h, "Bearer np_"))
	}
	if c, err := r.Cookie("np_session"); err == nil {
		return a.session(r.Context(), c.Value)
	}
	return nil, nil // anonymous
}

func (a *Authenticator) token(r *http.Request, body string) (*Principal, error) {
	if len(body) < tokenPrefixLen {
		return nil, fmt.Errorf("malformed token")
	}
	candidates, err := a.Store.TokensByPrefix(r.Context(), body[:tokenPrefixLen])
	if err != nil {
		return nil, err
	}
	for _, t := range candidates {
		if !VerifySecret(body, t.Hash) {
			continue
		}
		if t.ExpiresAt != nil && t.ExpiresAt.Before(time.Now()) {
			return nil, fmt.Errorf("token expired")
		}
		if len(t.IPBind) > 0 && !ipAllowed(remoteIP(r), t.IPBind) {
			return nil, fmt.Errorf("token not valid from this address")
		}
		perms := append([]model.Permission{}, t.Scopes...)
		perms = append(perms, a.rolePerms(r.Context(), t.TenantID, t.RoleNames)...)
		actorType := model.ActorToken
		if t.AIAgent {
			actorType = model.ActorAI
		}
		go a.Store.TouchAPIToken(context.WithoutCancel(r.Context()), t.ID)
		return &Principal{ActorType: actorType, ActorID: t.ID, Name: t.Name,
			TenantID: t.TenantID, Perms: perms, TokenID: t.ID}, nil
	}
	return nil, fmt.Errorf("invalid token")
}

func (a *Authenticator) session(ctx context.Context, id string) (*Principal, error) {
	userID, tenantID, data, err := a.Store.GetSession(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("session invalid")
	}
	user, err := a.Store.GetUser(ctx, userID)
	if err != nil || user.Disabled {
		return nil, fmt.Errorf("user invalid")
	}
	perms := a.rolePerms(ctx, tenantID, data.Roles)
	return &Principal{ActorType: model.ActorUser, ActorID: user.ID, Name: user.Name,
		TenantID: tenantID, Perms: perms, SessionID: id}, nil
}

// rolePerms expands role names (nested includes, A-15.07).
func (a *Authenticator) rolePerms(ctx context.Context, tenantID string, roleNames []string) []model.Permission {
	var perms []model.Permission
	seen := map[string]bool{}
	var expand func(names []string, depth int)
	expand = func(names []string, depth int) {
		if depth > 8 {
			return
		}
		for _, name := range names {
			if seen[name] {
				continue
			}
			seen[name] = true
			role, err := storage.LoadOne[model.Role](ctx, a.Store, tenantID, storage.KindRole, name)
			if err != nil {
				continue
			}
			perms = append(perms, role.Permissions...)
			expand(role.Includes, depth+1)
		}
	}
	expand(roleNames, 0)
	return perms
}

// NewSession mints a session for a logged-in user.
func (a *Authenticator) NewSession(ctx context.Context, userID, tenantID string,
	roles, groups []string, ttl time.Duration) (string, error) {
	id := base64.RawURLEncoding.EncodeToString([]byte(model.NewSecret(24)))
	err := a.Store.CreateSession(ctx, id, userID, tenantID,
		storage.SessionData{Roles: roles, Groups: groups}, ttl)
	return id, err
}

func remoteIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return net.ParseIP(host)
}

func ipAllowed(ip net.IP, cidrs []string) bool {
	if ip == nil {
		return false
	}
	for _, c := range cidrs {
		if !strings.Contains(c, "/") {
			if ip.Equal(net.ParseIP(c)) {
				return true
			}
			continue
		}
		_, n, err := net.ParseCIDR(c)
		if err == nil && n.Contains(ip) {
			return true
		}
	}
	return false
}

// AuthenticateToken resolves a bare token string (CLI/MCP-stdio use,
// outside an HTTP request).
func AuthenticateToken(ctx context.Context, a *Authenticator, token string) (*Principal, error) {
	body := strings.TrimPrefix(token, "np_")
	if len(body) < tokenPrefixLen {
		return nil, fmt.Errorf("malformed token")
	}
	candidates, err := a.Store.TokensByPrefix(ctx, body[:tokenPrefixLen])
	if err != nil {
		return nil, err
	}
	for _, t := range candidates {
		if !VerifySecret(body, t.Hash) {
			continue
		}
		if t.ExpiresAt != nil && t.ExpiresAt.Before(time.Now()) {
			return nil, fmt.Errorf("token expired")
		}
		perms := append([]model.Permission{}, t.Scopes...)
		perms = append(perms, a.rolePerms(ctx, t.TenantID, t.RoleNames)...)
		actorType := model.ActorToken
		if t.AIAgent {
			actorType = model.ActorAI
		}
		return &Principal{ActorType: actorType, ActorID: t.ID, Name: t.Name,
			TenantID: t.TenantID, Perms: perms, TokenID: t.ID}, nil
	}
	return nil, fmt.Errorf("invalid token")
}

// SecretsResolver adapts the store+box to the executor/notifier hooks.
func SecretsResolver(store *storage.Store, box *SecretBox) func(tenantID, name string) (string, bool) {
	return func(tenantID, name string) (string, bool) {
		if box == nil {
			return "", false
		}
		blob, err := store.GetSecret(context.Background(), tenantID, name)
		if err != nil {
			return "", false
		}
		v, err := box.Open(blob)
		if err != nil {
			return "", false
		}
		return v, true
	}
}
