// Package ldap mirrors directory users into the local user table and
// verifies directory logins (SPEC §11.2 extension). The directory is
// the authority for existence, name, e-mail and group→role assignment;
// Northplane never writes to the directory. Synced users carry the
// subject prefix "ldap|" and (like OIDC users) no local password —
// login is a search-then-bind against the directory.
//
// Two safety rules mirror the OIDC path: a locally disabled account is
// never resurrected by sync or login, and the break-glass local admin
// is untouched (sync only manages "ldap|" subjects).
package ldap

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	goldap "github.com/go-ldap/ldap/v3"

	"github.com/northplane/northplane/internal/config"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
)

// SubjectPrefix marks directory-managed user rows.
const SubjectPrefix = "ldap|"

// Conn is the slice of *goldap.Conn the syncer needs — narrow so tests
// can substitute an in-memory directory.
type Conn interface {
	Bind(username, password string) error
	Search(*goldap.SearchRequest) (*goldap.SearchResult, error)
	SearchWithPaging(*goldap.SearchRequest, uint32) (*goldap.SearchResult, error)
	Close() error
}

// Dialer opens a directory connection (overridable in tests).
type Dialer func(ctx context.Context, cfg config.LDAPConfig) (Conn, error)

// Result summarises one sync pass.
type Result struct {
	Created   int      `json:"created"`
	Updated   int      `json:"updated"`
	Unchanged int      `json:"unchanged"`
	Disabled  int      `json:"disabled"`
	Skipped   int      `json:"skipped"`
	Warnings  []string `json:"warnings,omitempty"`
}

// Status is the admin-visible sync state.
type Status struct {
	Configured   bool       `json:"configured"`
	URL          string     `json:"url,omitempty"`
	SyncInterval string     `json:"syncInterval,omitempty"`
	LastSyncAt   *time.Time `json:"lastSyncAt,omitempty"`
	LastError    string     `json:"lastError,omitempty"`
	LastResult   *Result    `json:"lastResult,omitempty"`
}

// Syncer owns the periodic sync loop and login verification.
type Syncer struct {
	Cfg   config.LDAPConfig
	Store *storage.Store
	Log   *slog.Logger
	Dial  Dialer // nil = real DialURL

	mu         sync.Mutex
	syncing    bool
	lastSync   *time.Time
	lastErr    string
	lastResult *Result
}

// New returns a Syncer, or nil when LDAP is unconfigured.
func New(cfg config.LDAPConfig, store *storage.Store, log *slog.Logger) *Syncer {
	if !cfg.Enabled() {
		return nil
	}
	return &Syncer{Cfg: cfg, Store: store, Log: log}
}

// dialReal connects per config: ldaps:// dials TLS, startTls upgrades
// a plaintext connection before any credential crosses the wire.
func dialReal(_ context.Context, cfg config.LDAPConfig) (Conn, error) {
	tlsCfg := &tls.Config{
		InsecureSkipVerify: cfg.InsecureSkipVerify, //nolint:gosec // operator opt-in for lab/self-signed directories
		MinVersion:         tls.VersionTLS12,
		ServerName:         hostOf(cfg.URL),
	}
	conn, err := goldap.DialURL(cfg.URL, goldap.DialWithTLSConfig(tlsCfg))
	if err != nil {
		return nil, err
	}
	if cfg.StartTLS && strings.HasPrefix(cfg.URL, "ldap://") {
		if err := conn.StartTLS(tlsCfg); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("starttls: %w", err)
		}
	}
	return conn, nil
}

func hostOf(url string) string {
	rest := url
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+3:]
	}
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		rest = rest[:i]
	}
	if i := strings.LastIndexByte(rest, ':'); i >= 0 {
		rest = rest[:i]
	}
	return rest
}

func (s *Syncer) dial(ctx context.Context) (Conn, error) {
	if s.Dial != nil {
		return s.Dial(ctx, s.Cfg)
	}
	return dialReal(ctx, s.Cfg)
}

// Run is the periodic sync loop (server-spawned worker).
func (s *Syncer) Run(ctx context.Context) {
	interval := s.Cfg.SyncInterval
	if interval <= 0 {
		interval = 15 * time.Minute
	}
	if _, err := s.SyncNow(ctx); err != nil {
		s.Log.Warn("ldap: initial sync failed", "err", err)
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := s.SyncNow(ctx); err != nil {
				s.Log.Warn("ldap: sync failed", "err", err)
			}
		}
	}
}

// Status returns the current sync state for the admin API.
func (s *Syncer) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Status{Configured: true, URL: s.Cfg.URL,
		SyncInterval: orInterval(s.Cfg.SyncInterval).String(),
		LastSyncAt:   s.lastSync, LastError: s.lastErr, LastResult: s.lastResult}
	return st
}

func orInterval(d time.Duration) time.Duration {
	if d <= 0 {
		return 15 * time.Minute
	}
	return d
}

// SyncNow performs one full reconciliation pass. Concurrent calls
// coalesce: a second caller gets an error instead of a duplicate pass.
func (s *Syncer) SyncNow(ctx context.Context) (Result, error) {
	s.mu.Lock()
	if s.syncing {
		s.mu.Unlock()
		return Result{}, fmt.Errorf("sync already running")
	}
	s.syncing = true
	s.mu.Unlock()

	res, err := s.syncPass(ctx)

	now := time.Now().UTC()
	s.mu.Lock()
	s.syncing = false
	s.lastSync = &now
	s.lastErr = ""
	if err != nil {
		s.lastErr = err.Error()
	} else {
		r := res
		s.lastResult = &r
	}
	s.mu.Unlock()
	return res, err
}

// directoryUser is one entry normalised from the search result.
type directoryUser struct {
	subject string
	name    string
	email   string
	roles   []string
}

func (s *Syncer) syncPass(ctx context.Context) (Result, error) {
	var res Result
	conn, err := s.dial(ctx)
	if err != nil {
		return res, fmt.Errorf("dial %s: %w", s.Cfg.URL, err)
	}
	defer func() { _ = conn.Close() }()
	if s.Cfg.BindDN != "" {
		if err := conn.Bind(s.Cfg.BindDN, s.Cfg.BindPassword); err != nil {
			return res, fmt.Errorf("bind %s: %w", s.Cfg.BindDN, err)
		}
	}

	roles, err := storage.LoadAll[model.Role](ctx, s.Store, model.DefaultTenant, storage.KindRole)
	if err != nil {
		return res, err
	}

	attrs := []string{"dn", s.Cfg.UserAttr, s.Cfg.NameAttr, s.Cfg.GroupAttr}
	if s.Cfg.IDAttr != "" {
		attrs = append(attrs, s.Cfg.IDAttr)
	}
	req := goldap.NewSearchRequest(s.Cfg.BaseDN, goldap.ScopeWholeSubtree,
		goldap.NeverDerefAliases, 0, 0, false, s.Cfg.UserFilter, attrs, nil)
	sr, err := conn.SearchWithPaging(req, 500)
	if err != nil {
		return res, fmt.Errorf("search %s: %w", s.Cfg.UserFilter, err)
	}

	desired := make([]directoryUser, 0, len(sr.Entries))
	seen := map[string]bool{}
	for _, e := range sr.Entries {
		du, warn := s.normalize(conn, e, roles)
		if warn != "" {
			res.Warnings = append(res.Warnings, warn)
			res.Skipped++
			continue
		}
		if seen[du.subject] {
			res.Warnings = append(res.Warnings, "duplicate subject "+du.subject)
			res.Skipped++
			continue
		}
		seen[du.subject] = true
		desired = append(desired, du)
	}

	existing, err := s.Store.ListUsers(ctx)
	if err != nil {
		return res, err
	}
	bySubject := map[string]*model.User{}
	for _, u := range existing {
		if strings.HasPrefix(u.Subject, SubjectPrefix) {
			bySubject[u.Subject] = u
		}
	}

	for _, du := range desired {
		cur := bySubject[du.subject]
		switch {
		case cur == nil:
			_, err := s.Store.CreateUser(ctx, &model.User{
				Name: du.name, Email: du.email, Subject: du.subject, Roles: du.roles})
			if err == storage.ErrDuplicate {
				res.Warnings = append(res.Warnings,
					fmt.Sprintf("%s: e-mail %s already taken by another account", du.subject, du.email))
				res.Skipped++
				continue
			}
			if err != nil {
				return res, err
			}
			res.Created++
		case cur.Disabled:
			// never resurrect a locally disabled account (single authority)
			res.Skipped++
		case cur.Name != du.name || cur.Email != du.email || !sameRoles(cur.Roles, du.roles):
			cur.Name, cur.Email, cur.Roles = du.name, du.email, du.roles
			if err := s.Store.UpdateUser(ctx, cur); err == storage.ErrDuplicate {
				res.Warnings = append(res.Warnings,
					fmt.Sprintf("%s: e-mail %s already taken by another account", du.subject, du.email))
				res.Skipped++
			} else if err != nil {
				return res, err
			} else {
				res.Updated++
			}
		default:
			res.Unchanged++
		}
	}

	if s.Cfg.DisableMissing {
		for subject, u := range bySubject {
			if seen[subject] || u.Disabled {
				continue
			}
			u.Disabled = true
			if err := s.Store.UpdateUser(ctx, u); err != nil {
				return res, err
			}
			res.Disabled++
		}
	}

	s.Log.Info("ldap: sync complete", "created", res.Created, "updated", res.Updated,
		"unchanged", res.Unchanged, "disabled", res.Disabled, "skipped", res.Skipped)
	return res, nil
}

// normalize converts one directory entry into the desired user row,
// including the group→role mapping. A non-empty warn skips the entry.
func (s *Syncer) normalize(conn Conn, e *goldap.Entry, roles []*model.Role) (directoryUser, string) {
	email := strings.TrimSpace(e.GetAttributeValue(s.Cfg.UserAttr))
	if email == "" {
		return directoryUser{}, e.DN + ": empty " + s.Cfg.UserAttr
	}
	name := strings.TrimSpace(e.GetAttributeValue(s.Cfg.NameAttr))
	if name == "" {
		name = email
	}
	subject := SubjectPrefix + strings.ToLower(e.DN)
	if s.Cfg.IDAttr != "" {
		if raw := e.GetRawAttributeValue(s.Cfg.IDAttr); len(raw) > 0 {
			if utf8.Valid(raw) {
				subject = SubjectPrefix + string(raw)
			} else {
				subject = SubjectPrefix + fmt.Sprintf("%x", raw) // binary GUIDs
			}
		}
	}
	groups := s.groupsOf(conn, e)
	return directoryUser{subject: subject, name: name, email: strings.ToLower(email),
		roles: s.mapGroups(groups, roles)}, ""
}

// groupsOf returns the entry's group identifiers: each group's full DN
// and its first RDN value (cn), lowercased, so Role.IdPGroups may hold
// either form.
func (s *Syncer) groupsOf(conn Conn, e *goldap.Entry) []string {
	var dns []string
	if s.Cfg.GroupFilter != "" {
		filter := strings.NewReplacer(
			"{dn}", goldap.EscapeFilter(e.DN),
			"{user}", goldap.EscapeFilter(e.GetAttributeValue(s.Cfg.UserAttr)),
		).Replace(s.Cfg.GroupFilter)
		base := s.Cfg.GroupBaseDN
		if base == "" {
			base = s.Cfg.BaseDN
		}
		req := goldap.NewSearchRequest(base, goldap.ScopeWholeSubtree,
			goldap.NeverDerefAliases, 0, 0, false, filter, []string{"dn"}, nil)
		if sr, err := conn.Search(req); err == nil {
			for _, g := range sr.Entries {
				dns = append(dns, g.DN)
			}
		} else {
			s.Log.Warn("ldap: group search failed", "filter", filter, "err", err)
		}
	} else {
		dns = e.GetAttributeValues(s.Cfg.GroupAttr)
	}
	var out []string
	for _, dn := range dns {
		out = append(out, strings.ToLower(dn))
		if cn := firstRDN(dn); cn != "" {
			out = append(out, strings.ToLower(cn))
		}
	}
	return out
}

// firstRDN extracts the value of the first RDN ("cn=admins,ou=…" → "admins").
func firstRDN(dn string) string {
	first := dn
	if i := strings.IndexByte(first, ','); i >= 0 {
		first = first[:i]
	}
	if _, v, ok := strings.Cut(first, "="); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// mapGroups resolves directory groups onto role names via
// Role.IdPGroups (shared with OIDC) plus the adminGroup bootstrap and
// the defaultRoles fallback.
func (s *Syncer) mapGroups(groups []string, roles []*model.Role) []string {
	set := map[string]bool{}
	for _, g := range groups {
		set[g] = true
	}
	var out []string
	for _, role := range roles {
		for _, g := range role.IdPGroups {
			if set[strings.ToLower(g)] {
				out = append(out, role.Name)
				break
			}
		}
	}
	if s.Cfg.AdminGroup != "" && set[strings.ToLower(s.Cfg.AdminGroup)] {
		out = append(out, "admin")
	}
	if len(out) == 0 {
		out = append(out, s.Cfg.DefaultRoles...)
	}
	sort.Strings(out)
	return dedupe(out)
}

func dedupe(in []string) []string {
	out := in[:0]
	var prev string
	for i, v := range in {
		if i == 0 || v != prev {
			out = append(out, v)
		}
		prev = v
	}
	return out
}

func sameRoles(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	as := append([]string(nil), a...)
	bs := append([]string(nil), b...)
	sort.Strings(as)
	sort.Strings(bs)
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}

// VerifyLogin authenticates a directory user with search-then-bind: a
// service-account search resolves the e-mail to the entry DN, then a
// bind as that DN proves the password. Returns nil on success.
// Empty passwords are rejected before anything touches the wire — an
// unauthenticated simple bind would otherwise "succeed" on most
// directories (RFC 4513 §5.1.2).
func (s *Syncer) VerifyLogin(ctx context.Context, email, password string) error {
	if password == "" {
		return fmt.Errorf("empty password")
	}
	conn, err := s.dial(ctx)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if s.Cfg.BindDN != "" {
		if err := conn.Bind(s.Cfg.BindDN, s.Cfg.BindPassword); err != nil {
			return fmt.Errorf("service bind: %w", err)
		}
	}
	filter := fmt.Sprintf("(&%s(%s=%s))", s.Cfg.UserFilter, s.Cfg.UserAttr,
		goldap.EscapeFilter(email))
	req := goldap.NewSearchRequest(s.Cfg.BaseDN, goldap.ScopeWholeSubtree,
		goldap.NeverDerefAliases, 2, 0, false, filter, []string{"dn"}, nil)
	sr, err := conn.Search(req)
	if err != nil {
		return fmt.Errorf("user search: %w", err)
	}
	if len(sr.Entries) != 1 {
		return fmt.Errorf("user not found (or ambiguous): %d entries", len(sr.Entries))
	}
	if err := conn.Bind(sr.Entries[0].DN, password); err != nil {
		return fmt.Errorf("bind: %w", err)
	}
	return nil
}
