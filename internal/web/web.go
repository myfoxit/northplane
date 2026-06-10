// Package web delivers the frontend: the React SPA from go:embed plus
// the two deliberately server-rendered pages — login and the public
// status page (robust, JS-free; SPEC §12.1/§12.4).
package web

import (
	"context"
	"crypto/subtle"
	"embed"
	"html/template"
	"io/fs"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/northplane/northplane/internal/auth"
	"github.com/northplane/northplane/internal/config"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
)

//go:embed all:dist
var distFS embed.FS

// SPAHandler serves the embedded UI with immutable caching for hashed
// assets and index.html fallback for client-side routes (SPEC §12.2).
func SPAHandler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "UI not embedded in this build — run `make web` before building", http.StatusNotImplemented)
		})
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		f, err := sub.Open(path)
		if err != nil {
			// client-side route → index.html (no cache)
			w.Header().Set("Cache-Control", "no-cache")
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/"
			fileServer.ServeHTTP(w, r2)
			return
		}
		_ = f.Close() // opened only to test existence
		if strings.HasPrefix(path, "assets/") {
			// hashed filenames → immutable (SPEC §12.2)
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		fileServer.ServeHTTP(w, r)
	})
}

// Pages renders login/auth/status.
type Pages struct {
	store     *storage.Store
	auth      *auth.Authenticator
	oidc      *auth.OIDC
	directory DirectoryVerifier
	cfg       config.Config
	version   string
}

// DirectoryVerifier authenticates a directory (LDAP) user — implemented
// by *ldap.Syncer; nil when no directory is configured.
type DirectoryVerifier interface {
	VerifyLogin(ctx context.Context, email, password string) error
}

// NewPages builds the server-rendered page handler.
func NewPages(store *storage.Store, authn *auth.Authenticator, oidc *auth.OIDC,
	directory DirectoryVerifier, cfg config.Config, version string) *Pages {
	return &Pages{store: store, auth: authn, oidc: oidc, directory: directory,
		cfg: cfg, version: version}
}

// dummyPassHash is a real argon2id hash verified against unknown/non-local
// accounts so failed logins take the same time regardless of whether the
// account exists (anti-enumeration). Computed once at startup.
var dummyPassHash = auth.HashSecret("northplane-nonexistent-account-placeholder")

// loginLimiter throttles local-login attempts per client IP to blunt
// online password brute-forcing of the break-glass admin accounts.
type loginLimiter struct {
	mu      sync.Mutex
	buckets map[string]*loginBucket
}

type loginBucket struct {
	tokens float64
	last   time.Time
}

const (
	loginBurst  = 8.0        // attempts before throttling kicks in
	loginRefill = 1.0 / 15.0 // 1 token / 15s ⇒ ~4 attempts/min sustained
)

var logins = &loginLimiter{buckets: map[string]*loginBucket{}}

// setupMu serialises the first-run gate check + admin creation so two
// concurrent POST /setup cannot both pass the zero-users check and create
// duplicate admin accounts. Single process ⇒ a plain Mutex suffices.
var setupMu sync.Mutex

// minSetupPasswordLen is the minimum length for the initial admin password.
const minSetupPasswordLen = 12

// Session lifetimes. A normal login lasts sessionTTL; ticking "remember me"
// extends it to rememberTTL and makes the cookie persist across browser
// restarts (longer MaxAge). Both are DB-backed, so either way the session
// also survives a server restart.
const (
	sessionTTL  = 12 * time.Hour
	rememberTTL = 30 * 24 * time.Hour
)

// FirstRunOpen reports whether the install is fresh enough to expose the
// /setup page: no local (break-glass) user and no API token exists yet.
// SSO-provisioned users do not close the gate — an OIDC install may still
// need a local break-glass admin. Fails closed on storage errors so a
// transient DB problem never exposes account creation.
func FirstRunOpen(ctx context.Context, store *storage.Store) bool {
	users, err := store.ListUsers(ctx)
	if err != nil {
		return false
	}
	for _, u := range users {
		if u.Local {
			return false
		}
	}
	toks, err := store.ListAPITokens(ctx, model.DefaultTenant)
	if err != nil || len(toks) > 0 {
		return false
	}
	return true
}

func (p *Pages) firstRunOpen(ctx context.Context) bool { return FirstRunOpen(ctx, p.store) }

// allow reports whether an attempt from ip may proceed, refilling the
// token bucket based on elapsed time. now is injected for testability.
func (l *loginLimiter) allow(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.buckets[ip]
	if b == nil {
		b = &loginBucket{tokens: loginBurst, last: now}
		l.buckets[ip] = b
	}
	b.tokens += now.Sub(b.last).Seconds() * loginRefill
	if b.tokens > loginBurst {
		b.tokens = loginBurst
	}
	b.last = now
	// opportunistic GC so the map can't grow without bound
	if len(l.buckets) > 4096 {
		for k, v := range l.buckets {
			if now.Sub(v.last) > time.Hour {
				delete(l.buckets, k)
			}
		}
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (p *Pages) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/login" && r.Method == http.MethodGet:
		// fresh install → every login entry path lands on first-run setup
		if p.firstRunOpen(r.Context()) {
			http.Redirect(w, r, "/setup", http.StatusFound)
			return
		}
		p.loginPage(w, r, "")
	case r.URL.Path == "/login" && r.Method == http.MethodPost:
		p.localLogin(w, r)
	case r.URL.Path == "/setup" && r.Method == http.MethodGet:
		p.setupPage(w, r, "")
	case r.URL.Path == "/setup" && r.Method == http.MethodPost:
		p.setupSubmit(w, r)
	case r.URL.Path == "/auth/oidc":
		if p.oidc == nil {
			http.Error(w, "SSO not configured", http.StatusNotImplemented)
			return
		}
		p.oidc.Start(w, r)
	case r.URL.Path == "/auth/callback":
		p.callback(w, r)
	case r.URL.Path == "/auth/logout":
		p.logout(w, r)
	case strings.HasPrefix(r.URL.Path, "/status/"):
		p.statusPage(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (p *Pages) localLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		p.loginPage(w, r, "Ungültige Anfrage.")
		return
	}
	if !logins.allow(remoteIP(r), time.Now()) {
		w.Header().Set("Retry-After", "30")
		p.loginPage(w, r, "Zu viele Anmeldeversuche. Bitte kurz warten.")
		return
	}
	email := r.FormValue("email")
	password := r.FormValue("password")
	user, err := p.store.GetUserByEmail(r.Context(), email)

	// Directory-managed accounts (subject "ldap|…") verify against the
	// directory itself: search-then-bind, no local hash. Roles come from
	// the last sync (the directory is the assignment authority).
	if err == nil && !user.Local && p.directory != nil &&
		strings.HasPrefix(user.Subject, "ldap|") {
		if verr := p.directory.VerifyLogin(r.Context(), email, password); verr != nil {
			p.loginPage(w, r, "Anmeldung fehlgeschlagen.")
			return
		}
		roles := user.Roles
		if len(roles) == 0 {
			roles = []string{"viewer"}
		}
		p.finishLogin(w, r, user, roles, "login.ldap")
		return
	}

	// Always run the (expensive) argon2 verification — against the real
	// hash when the user exists & is local, otherwise a fixed dummy — so
	// login timing does not reveal whether an account exists (user
	// enumeration). A plain short-circuit would skip argon2 for unknown
	// users and make them measurably faster than wrong-password attempts.
	hash := dummyPassHash
	if err == nil && user.Local && user.PassHash != "" {
		hash = user.PassHash
	}
	valid := auth.VerifySecret(password, hash)
	if err != nil || !user.Local || !valid {
		// Generic message for wrong password, unknown account AND disabled
		// account (GetUserByEmail already filters disabled = false) so login
		// never reveals which case it was — no user enumeration (SPEC §13.2).
		p.loginPage(w, r, "Anmeldung fehlgeschlagen.")
		return
	}
	// Authoritative roles live on the account now; fall back to admin only
	// for legacy rows provisioned before user-bound roles existed (so a
	// pre-migration break-glass admin never loses access).
	roles := user.Roles
	if len(roles) == 0 {
		roles = []string{"admin"}
	}
	p.finishLogin(w, r, user, roles, "login.local")
}

// finishLogin mints the session, audits and redirects — shared by the
// local-password and directory login paths. "Remember me" trades a
// longer-lived, browser-persistent session for convenience; the default
// stays short so a shared/forgotten browser logs out the same day.
func (p *Pages) finishLogin(w http.ResponseWriter, r *http.Request,
	user *model.User, roles []string, action string) {
	ttl := sessionTTL
	if r.FormValue("remember") != "" {
		ttl = rememberTTL
	}
	session, err := p.auth.NewSession(r.Context(), user.ID, model.DefaultTenant, roles, nil, ttl)
	if err != nil {
		p.loginPage(w, r, "Interner Fehler.")
		return
	}
	p.setSession(w, r, session, ttl)
	_, _ = p.store.AppendAudit(r.Context(), &model.AuditEntry{
		TenantID: model.DefaultTenant, ActorType: model.ActorUser, ActorID: user.ID,
		Action: action, SourceIP: remoteIP(r),
	})
	http.Redirect(w, r, "/", http.StatusFound)
}

func (p *Pages) callback(w http.ResponseWriter, r *http.Request) {
	if p.oidc == nil {
		http.Error(w, "SSO not configured", http.StatusNotImplemented)
		return
	}
	session, err := p.oidc.Callback(w, r)
	if err != nil {
		p.loginPage(w, r, "SSO-Anmeldung fehlgeschlagen: "+err.Error())
		return
	}
	p.setSession(w, r, session, sessionTTL)
	http.Redirect(w, r, "/", http.StatusFound)
}

func (p *Pages) setSession(w http.ResponseWriter, r *http.Request, session string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name: "np_session", Value: session, Path: "/",
		HttpOnly: true, Secure: auth.RequestIsHTTPS(r, p.cfg.TrustProxy), SameSite: http.SameSiteLaxMode,
		MaxAge: int(ttl.Seconds()),
	})
}

func (p *Pages) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("np_session"); err == nil {
		_ = p.store.DeleteSession(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: "np_session", Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusFound)
}

func remoteIP(r *http.Request) string {
	host := r.RemoteAddr
	if i := strings.LastIndexByte(host, ':'); i > 0 {
		host = host[:i]
	}
	return host
}

var loginTpl = template.Must(template.New("login").Parse(`<!doctype html>
<html lang="de"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Northplane — Anmeldung</title>
<style>
:root{color-scheme:dark}
body{font-family:system-ui,-apple-system,sans-serif;background:#0b1220;color:#e2e8f0;display:grid;place-items:center;min-height:100vh;margin:0}
.card{background:#111a2e;border:1px solid #1e293b;border-radius:12px;padding:2.2rem;width:20rem}
h1{font-size:1.15rem;margin:0 0 1.4rem;display:flex;align-items:center;gap:.5rem}
h1 b{color:#60a5fa}
label{display:block;font-size:.8rem;color:#94a3b8;margin:.8rem 0 .25rem}
input{width:100%;box-sizing:border-box;background:#0b1220;border:1px solid #334155;border-radius:8px;color:#e2e8f0;padding:.55rem .7rem;font-size:.9rem}
button{width:100%;margin-top:1.2rem;background:#2563eb;border:0;border-radius:8px;color:#fff;padding:.6rem;font-size:.9rem;cursor:pointer}
button:hover{background:#1d4ed8}
.sso{background:#1e293b;margin-top:.6rem}
.err{background:#7f1d1d;border-radius:8px;padding:.55rem .7rem;font-size:.82rem;margin-bottom:1rem}
.v{color:#475569;font-size:.7rem;text-align:center;margin-top:1.2rem}
</style></head><body>
<form class="card" method="post" action="/login">
  <h1><b>▲</b> Northplane</h1>
  {{if .Error}}<div class="err">{{.Error}}</div>{{end}}
  <label for="email">E-Mail</label>
  <input id="email" name="email" type="email" autocomplete="username" required>
  <label for="password">Passwort</label>
  <input id="password" name="password" type="password" autocomplete="current-password" required>
  <label style="display:flex;align-items:center;gap:.45rem;font-size:.8rem;color:#94a3b8;margin-top:.9rem"><input type="checkbox" name="remember" value="1" style="width:auto;margin:0"> Angemeldet bleiben</label>
  <button type="submit">Anmelden</button>
  {{if .SSO}}<button type="button" class="sso" onclick="location.href='/auth/oidc'">Single Sign-On</button>{{end}}
  <div class="v">{{.Version}}</div>
</form></body></html>`))

func (p *Pages) loginPage(w http.ResponseWriter, r *http.Request, errMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if errMsg != "" {
		w.WriteHeader(http.StatusUnauthorized)
	}
	_ = loginTpl.Execute(w, map[string]any{
		"Error": errMsg, "SSO": p.oidc != nil, "Version": p.version,
	})
}

// --- first-run setup (one-shot initial admin account) ---

var setupTpl = template.Must(template.New("setup").Parse(`<!doctype html>
<html lang="de"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Northplane — Einrichtung</title>
<style>
:root{color-scheme:dark}
body{font-family:system-ui,-apple-system,sans-serif;background:#0b1220;color:#e2e8f0;display:grid;place-items:center;min-height:100vh;margin:0}
.card{background:#111a2e;border:1px solid #1e293b;border-radius:12px;padding:2.2rem;width:20rem}
h1{font-size:1.15rem;margin:0 0 .4rem;display:flex;align-items:center;gap:.5rem}
h1 b{color:#60a5fa}
p.hint{font-size:.8rem;color:#94a3b8;margin:0 0 1.2rem}
label{display:block;font-size:.8rem;color:#94a3b8;margin:.8rem 0 .25rem}
input{width:100%;box-sizing:border-box;background:#0b1220;border:1px solid #334155;border-radius:8px;color:#e2e8f0;padding:.55rem .7rem;font-size:.9rem}
button{width:100%;margin-top:1.2rem;background:#2563eb;border:0;border-radius:8px;color:#fff;padding:.6rem;font-size:.9rem;cursor:pointer}
button:hover{background:#1d4ed8}
.sso{background:#1e293b;margin-top:.6rem}
.err{background:#7f1d1d;border-radius:8px;padding:.55rem .7rem;font-size:.82rem;margin-bottom:1rem}
.v{color:#475569;font-size:.7rem;text-align:center;margin-top:1.2rem}
</style></head><body>
<form class="card" method="post" action="/setup">
  <h1><b>▲</b> Northplane</h1>
  <p class="hint">Erstkonfiguration — Administrator-Konto anlegen</p>
  {{if .Error}}<div class="err">{{.Error}}</div>{{end}}
  <label for="name">Name</label>
  <input id="name" name="name" type="text" autocomplete="name" required>
  <label for="email">E-Mail</label>
  <input id="email" name="email" type="email" autocomplete="username" required>
  <label for="password">Passwort (mind. 12 Zeichen)</label>
  <input id="password" name="password" type="password" autocomplete="new-password" minlength="12" required>
  <label for="confirm">Passwort bestätigen</label>
  <input id="confirm" name="confirm" type="password" autocomplete="new-password" minlength="12" required>
  <button type="submit">Konto erstellen</button>
  {{if .SSO}}<button type="button" class="sso" onclick="location.href='/auth/oidc'">Single Sign-On</button>{{end}}
  <div class="v">{{.Version}}</div>
</form></body></html>`))

func (p *Pages) setupPage(w http.ResponseWriter, r *http.Request, errMsg string) {
	if !p.firstRunOpen(r.Context()) {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if errMsg != "" {
		w.WriteHeader(http.StatusBadRequest)
	}
	_ = setupTpl.Execute(w, map[string]any{
		"Error": errMsg, "SSO": p.oidc != nil, "Version": p.version,
	})
}

// setupSubmit creates the initial admin account. Mirrors localLogin:
// shared rate limiter, German messages, audit entry, session cookie.
// No CSRF token — deliberately symmetric with the login POST (SameSite=Lax
// cookie; there is no ambient session to ride during first-run).
func (p *Pages) setupSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		p.setupPage(w, r, "Ungültige Anfrage.")
		return
	}
	if !logins.allow(remoteIP(r), time.Now()) {
		w.Header().Set("Retry-After", "30")
		p.setupPage(w, r, "Zu viele Versuche. Bitte kurz warten.")
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	email := strings.TrimSpace(r.FormValue("email"))
	password, confirm := r.FormValue("password"), r.FormValue("confirm")
	switch {
	case name == "" || email == "":
		p.setupPage(w, r, "Name und E-Mail sind erforderlich.")
		return
	case password != confirm:
		p.setupPage(w, r, "Passwörter stimmen nicht überein.")
		return
	case len([]rune(password)) < minSetupPasswordLen:
		p.setupPage(w, r, "Passwort muss mindestens 12 Zeichen haben.")
		return
	}

	setupMu.Lock()
	defer setupMu.Unlock()
	if !p.firstRunOpen(r.Context()) { // re-check inside the lock (double-POST race)
		http.Error(w, "setup already completed", http.StatusConflict)
		return
	}
	user, err := p.store.CreateLocalUser(r.Context(), name, email, auth.HashSecret(password))
	if err != nil {
		p.setupPage(w, r, "Konto konnte nicht angelegt werden.")
		return
	}
	roles := []string{"admin"} // break-glass local accounts are admins
	session, err := p.auth.NewSession(r.Context(), user.ID, model.DefaultTenant, roles, nil, sessionTTL)
	if err != nil {
		p.setupPage(w, r, "Interner Fehler.")
		return
	}
	p.setSession(w, r, session, sessionTTL)
	_, _ = p.store.AppendAudit(r.Context(), &model.AuditEntry{
		TenantID: model.DefaultTenant, ActorType: model.ActorUser, ActorID: user.ID,
		Action: "setup.admin", SourceIP: remoteIP(r),
	})
	http.Redirect(w, r, "/", http.StatusFound)
}

// --- public status page (SPEC §12.4: server-rendered, JS-free) ---

type statusRow struct {
	Name  string
	State string
	Class string
}

var statusTpl = template.Must(template.New("status").Parse(`<!doctype html>
<html lang="de"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta http-equiv="refresh" content="60">
<title>{{.Title}} — Status</title>
<style>
:root{color-scheme:dark}
body{font-family:system-ui,sans-serif;background:#0b1220;color:#e2e8f0;max-width:42rem;margin:0 auto;padding:2rem 1rem}
h1{font-size:1.3rem}
.banner{border-radius:10px;padding:1rem 1.2rem;margin:1.2rem 0;font-weight:600}
.ok{background:#064e3b;color:#6ee7b7}.warn{background:#78350f;color:#fcd34d}.crit{background:#7f1d1d;color:#fca5a5}
.row{display:flex;justify-content:space-between;padding:.7rem .4rem;border-bottom:1px solid #1e293b}
.state{font-weight:600}.state.ok{color:#34d399}.state.warn{color:#fbbf24}.state.crit{color:#f87171}.state.unknown{color:#94a3b8}
.f{color:#475569;font-size:.75rem;margin-top:2rem;text-align:center}
</style></head><body>
<h1>{{.Title}}</h1>
<div class="banner {{.OverallClass}}">{{.OverallText}}</div>
{{range .Rows}}<div class="row"><span>{{.Name}}</span><span class="state {{.Class}}">{{.State}}</span></div>{{end}}
<div class="f">Stand: {{.Now}} · aktualisiert automatisch · Northplane</div>
</body></html>`))

// statusPage renders /status/{slug}. The slug "default" lists the
// tenant's business services; non-public pages require ?token= matching
// the kv-stored page config.
func (p *Pages) statusPage(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimPrefix(r.URL.Path, "/status/")
	if slug == "" {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	var pageCfg struct {
		Tenant string `json:"tenant"`
		Title  string `json:"title"`
		Token  string `json:"token"`
		Public bool   `json:"public"`
	}
	if err := p.store.KVGet(ctx, "statuspage/"+slug, &pageCfg); err != nil {
		if slug != "default" {
			http.NotFound(w, r)
			return
		}
		pageCfg.Tenant, pageCfg.Title, pageCfg.Public = model.DefaultTenant, "Service Status", true
	}
	if !pageCfg.Public {
		// Fail closed: a private page with no configured token denies all
		// (an empty == empty comparison would otherwise grant access).
		// Constant-time compare avoids leaking the token via timing.
		given := r.URL.Query().Get("token")
		if pageCfg.Token == "" || subtle.ConstantTimeCompare([]byte(given), []byte(pageCfg.Token)) != 1 {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	}

	rows, worst := p.statusRows(ctx, pageCfg.Tenant)
	overallText, overallClass := "Alle Systeme betriebsbereit", "ok"
	switch worst {
	case "crit":
		overallText, overallClass = "Störung — wir arbeiten daran", "crit"
	case "warn":
		overallText, overallClass = "Beeinträchtigter Betrieb", "warn"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "max-age=30, public")
	_ = statusTpl.Execute(w, map[string]any{
		"Title": pageCfg.Title, "Rows": rows,
		"OverallText": overallText, "OverallClass": overallClass,
		"Now": time.Now().UTC().Format("2006-01-02 15:04 UTC"),
	})
}

// statusRows lists business-service roots (fallback: host summary).
func (p *Pages) statusRows(ctx context.Context, tenantID string) ([]statusRow, string) {
	worst := "ok"
	bump := func(c string) {
		if c == "crit" || (c == "warn" && worst == "ok") {
			worst = c
		}
	}
	services, err := storage.LoadAll[model.BusinessService](ctx, p.store, tenantID,
		storage.KindBusinessService)
	if err == nil && len(services) > 0 {
		var rows []statusRow
		for _, bs := range services {
			if bs.ParentID != "" {
				continue // roots only on the public page
			}
			state, class := "Betriebsbereit", "ok"
			if bs.ObjectID != "" {
				if cs, err := p.store.GetCheckState(ctx, bs.ObjectID); err == nil {
					switch cs.State {
					case model.StateOK:
					case model.StateWarning:
						state, class = "Beeinträchtigt", "warn"
					default:
						state, class = "Störung", "crit"
					}
				}
			}
			bump(class)
			rows = append(rows, statusRow{Name: bs.Name, State: state, Class: class})
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
		return rows, worst
	}
	// fallback: aggregate host summary
	sum, err := p.store.Summary(ctx, tenantID)
	if err != nil {
		return nil, "ok"
	}
	rows := []statusRow{}
	switch {
	case sum.HostsDown > 0 || sum.ServicesCritical > 0:
		bump("crit")
		rows = append(rows, statusRow{Name: "Infrastruktur", State: "Störung", Class: "crit"})
	case sum.ServicesWarning > 0:
		bump("warn")
		rows = append(rows, statusRow{Name: "Infrastruktur", State: "Beeinträchtigt", Class: "warn"})
	default:
		rows = append(rows, statusRow{Name: "Infrastruktur", State: "Betriebsbereit", Class: "ok"})
	}
	return rows, worst
}
