package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/northplane/northplane/internal/config"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
)

// OIDC implements Authorization Code + PKCE SSO (SPEC §11.2; Entra ID
// and Keycloak are the tested providers).
type OIDC struct {
	cfg        config.OIDCConfig
	provider   *gooidc.Provider
	verifier   *gooidc.IDTokenVerifier
	oauth      oauth2.Config
	store      *storage.Store
	auth       *Authenticator
	trustProxy bool
}

// NewOIDC initialises the provider (nil when unconfigured).
func NewOIDC(ctx context.Context, cfg config.OIDCConfig, baseURL string,
	store *storage.Store, a *Authenticator, trustProxy bool) (*OIDC, error) {
	if cfg.Issuer == "" {
		return nil, nil
	}
	provider, err := gooidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery: %w", err)
	}
	scopes := cfg.Scopes
	if len(scopes) == 0 {
		scopes = []string{gooidc.ScopeOpenID, "profile", "email"}
	}
	return &OIDC{
		cfg:      cfg,
		provider: provider,
		verifier: provider.Verifier(&gooidc.Config{ClientID: cfg.ClientID}),
		oauth: oauth2.Config{
			ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret,
			Endpoint: provider.Endpoint(), Scopes: scopes,
			RedirectURL: baseURL + "/auth/callback",
		},
		store: store, auth: a, trustProxy: trustProxy,
	}, nil
}

// Start redirects to the IdP with PKCE + state cookies.
func (o *OIDC) Start(w http.ResponseWriter, r *http.Request) {
	state := randB64(24)
	verifier := randB64(48)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	secure := RequestIsHTTPS(r, o.trustProxy)
	http.SetCookie(w, &http.Cookie{Name: "np_oidc_state", Value: state,
		Path: "/auth", HttpOnly: true, Secure: secure, MaxAge: 600, SameSite: http.SameSiteLaxMode})
	http.SetCookie(w, &http.Cookie{Name: "np_oidc_verifier", Value: verifier,
		Path: "/auth", HttpOnly: true, Secure: secure, MaxAge: 600, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, o.oauth.AuthCodeURL(state,
		oauth2.SetAuthURLParam("code_challenge", challenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	), http.StatusFound)
}

// Callback exchanges the code, provisions the user and mints a session.
func (o *OIDC) Callback(w http.ResponseWriter, r *http.Request) (string, error) {
	stateCookie, err := r.Cookie("np_oidc_state")
	if err != nil || stateCookie.Value != r.URL.Query().Get("state") {
		return "", fmt.Errorf("state mismatch")
	}
	verifierCookie, err := r.Cookie("np_oidc_verifier")
	if err != nil {
		return "", fmt.Errorf("missing PKCE verifier")
	}
	tok, err := o.oauth.Exchange(r.Context(), r.URL.Query().Get("code"),
		oauth2.SetAuthURLParam("code_verifier", verifierCookie.Value))
	if err != nil {
		return "", fmt.Errorf("token exchange: %w", err)
	}
	rawID, _ := tok.Extra("id_token").(string)
	if rawID == "" {
		return "", fmt.Errorf("no id_token in response")
	}
	idToken, err := o.verifier.Verify(r.Context(), rawID)
	if err != nil {
		return "", fmt.Errorf("id_token verify: %w", err)
	}
	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		return "", err
	}
	name, _ := claims["name"].(string)
	email, _ := claims["email"].(string)
	if name == "" {
		name = email
	}
	subject := idToken.Issuer + "|" + idToken.Subject

	user, err := o.store.UpsertUserBySubject(r.Context(), subject, name, email)
	if err != nil {
		return "", err
	}
	// IdP group → role mapping (SPEC §11.2)
	groups := stringSlice(claims[orDefault(o.cfg.GroupsClaim, "groups")])
	roles := o.mapGroups(r.Context(), groups)
	if o.cfg.AdminGroup != "" {
		for _, g := range groups {
			if g == o.cfg.AdminGroup {
				roles = append(roles, "admin")
			}
		}
	}
	if len(roles) == 0 {
		roles = []string{"viewer"}
	}
	session, err := o.auth.NewSession(r.Context(), user.ID, model.DefaultTenant,
		roles, groups, 12*time.Hour)
	if err != nil {
		return "", err
	}
	return session, nil
}

// mapGroups finds roles whose idpGroups contain any of the user's groups.
func (o *OIDC) mapGroups(ctx context.Context, groups []string) []string {
	if len(groups) == 0 {
		return nil
	}
	set := map[string]bool{}
	for _, g := range groups {
		set[g] = true
	}
	roles, err := storage.LoadAll[model.Role](ctx, o.store, model.DefaultTenant, storage.KindRole)
	if err != nil {
		return nil
	}
	var out []string
	for _, role := range roles {
		for _, g := range role.IdPGroups {
			if set[g] {
				out = append(out, role.Name)
				break
			}
		}
	}
	return out
}

// LogoutURL builds the RP-initiated logout target when supported.
func (o *OIDC) LogoutURL(postLogout string) string {
	var meta struct {
		EndSession string `json:"end_session_endpoint"`
	}
	if err := o.provider.Claims(&meta); err != nil || meta.EndSession == "" {
		return postLogout
	}
	return meta.EndSession + "?post_logout_redirect_uri=" + url.QueryEscape(postLogout)
}

func stringSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, x := range arr {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func randB64(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
