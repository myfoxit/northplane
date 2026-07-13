package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/northplane/northplane/internal/auth"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
)

func (a *API) registerAdmin() {
	// whoami: effective rights disclosure (SPEC §11.2, Revisions-Anforderung)
	type whoami struct {
		ActorType   model.ActorType    `json:"actorType"`
		ActorID     string             `json:"actorId"`
		Name        string             `json:"name"`
		TenantID    string             `json:"tenantId"`
		Permissions []model.Permission `json:"permissions"`
	}
	a.handle("GET /api/v1/whoami", "Effective identity and permissions", "", nil, whoami{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			if p == nil {
				a.problem(w, r, http.StatusUnauthorized, "np:auth/required", "authentication required", "")
				return
			}
			a.writeJSON(w, http.StatusOK, whoami{ActorType: p.ActorType, ActorID: p.ActorID,
				Name: p.Name, TenantID: p.TenantID, Permissions: p.Perms})
		})

	// tenants
	a.handle("GET /api/v1/tenants", "List tenants", "admin:tenants", nil, listResponse{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			tenants, err := a.Store.Tenants(r.Context())
			if err != nil {
				a.fail(w, r, err)
				return
			}
			a.writeList(w, tenants, "")
		})
	type tenantBody struct {
		Name string `json:"name"`
		Slug string `json:"slug"`
	}
	a.handle("POST /api/v1/tenants", "Create tenant", "admin:tenants", tenantBody{}, nil,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			var req tenantBody
			if !a.decode(w, r, &req) {
				return
			}
			if req.Name == "" || req.Slug == "" {
				a.validationError(w, r, "tenant", "name and slug required")
				return
			}
			id := model.NewID()
			if err := a.Store.CreateTenant(r.Context(), id, req.Name, req.Slug); err != nil {
				a.fail(w, r, err)
				return
			}
			a.audit(r, p, "tenant.create", id, nil, req)
			a.writeJSON(w, http.StatusCreated, map[string]string{"id": id})
		})

	// users (SPEC §11.2 / §13.2). Local accounts are break-glass: created,
	// reset and deleted here; the last enabled local admin is protected so
	// an install can never lock itself out of its own administration.
	a.registerUsers()

	// roles
	a.resourceCRUD("roles", storage.KindRole, "admin", model.Role{})

	// API tokens (SPEC §11.2): secret shown exactly once.
	type tokenRequest struct {
		Name      string             `json:"name"`
		Scopes    []model.Permission `json:"scopes,omitempty"`
		Roles     []string           `json:"roles,omitempty"`
		IPBind    []string           `json:"ipBind,omitempty"`
		AIAgent   bool               `json:"aiAgent,omitempty"`
		ExpiresAt *time.Time         `json:"expiresAt,omitempty"`
	}
	type tokenResponse struct {
		Token string         `json:"token"` // shown once
		Meta  model.APIToken `json:"meta"`
	}
	a.handle("POST /api/v1/api-tokens", "Create API token (secret shown once)",
		"admin:tokens", tokenRequest{}, tokenResponse{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			var req tokenRequest
			if !a.decode(w, r, &req) {
				return
			}
			if req.Name == "" || (len(req.Scopes) == 0 && len(req.Roles) == 0) {
				a.validationError(w, r, "token", "name and scopes or roles required")
				return
			}
			clear, tok := auth.MintToken(a.tenantOf(r, p), req.Name, req.Scopes, &model.APIToken{
				RoleNames: req.Roles, IPBind: req.IPBind, AIAgent: req.AIAgent,
				ExpiresAt: req.ExpiresAt, CreatedBy: p.Name,
			})
			if err := a.Store.CreateAPIToken(r.Context(), tok); err != nil {
				a.fail(w, r, err)
				return
			}
			a.audit(r, p, "token.create", tok.ID, nil, map[string]any{
				"name": tok.Name, "scopes": tok.Scopes, "roles": tok.RoleNames, "aiAgent": tok.AIAgent})
			a.writeJSON(w, http.StatusCreated, tokenResponse{Token: clear, Meta: *tok})
		})

	a.handle("GET /api/v1/api-tokens", "List API tokens (no secrets)", "admin:tokens", nil, listResponse{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			toks, err := a.Store.ListAPITokens(r.Context(), a.tenantOf(r, p))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			a.writeList(w, toks, "")
		})

	a.handle("DELETE /api/v1/api-tokens/{id}", "Revoke API token", "admin:tokens", nil, nil,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			if err := a.Store.DeleteAPIToken(r.Context(), a.tenantOf(r, p), param(r, "id")); err != nil {
				a.fail(w, r, err)
				return
			}
			a.audit(r, p, "token.revoke", param(r, "id"), nil, nil)
			w.WriteHeader(http.StatusNoContent)
		})

	// rotation: mint a replacement, revoke the old one
	a.handle("POST /api/v1/api-tokens/{id}:rotate", "Rotate token (new secret, old revoked)",
		"admin:tokens", nil, tokenResponse{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			tenant := a.tenantOf(r, p)
			toks, err := a.Store.ListAPITokens(r.Context(), tenant)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			var old *model.APIToken
			for _, t := range toks {
				if t.ID == param(r, "id") {
					old = t
				}
			}
			if old == nil {
				a.problem(w, r, http.StatusNotFound, "np:not-found", "token not found", "")
				return
			}
			clear, tok := auth.MintToken(tenant, old.Name, old.Scopes, &model.APIToken{
				RoleNames: old.RoleNames, IPBind: old.IPBind, AIAgent: old.AIAgent,
				ExpiresAt: old.ExpiresAt, CreatedBy: p.Name,
			})
			if err := a.Store.CreateAPIToken(r.Context(), tok); err != nil {
				a.fail(w, r, err)
				return
			}
			_ = a.Store.DeleteAPIToken(r.Context(), tenant, old.ID)
			a.audit(r, p, "token.rotate", old.ID, nil, map[string]string{"newId": tok.ID})
			a.writeJSON(w, http.StatusOK, tokenResponse{Token: clear, Meta: *tok})
		})

	// secrets ($SECRET:name$, SPEC §8.2): write-only values
	type secretBody struct {
		Value string `json:"value"`
	}
	a.handle("PUT /api/v1/secrets/{name}", "Store an encrypted secret", "admin:secrets",
		secretBody{}, nil,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			if a.Box == nil {
				a.problem(w, r, http.StatusServiceUnavailable, "np:secrets/nokey",
					"secret store needs secretKeyFile in config", "")
				return
			}
			var req secretBody
			if !a.decode(w, r, &req) {
				return
			}
			blob, err := a.Box.Seal(req.Value)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			if err := a.Store.PutSecret(r.Context(), a.tenantOf(r, p), param(r, "name"), blob, p.Name); err != nil {
				a.fail(w, r, err)
				return
			}
			a.audit(r, p, "secret.put", param(r, "name"), nil, nil) // value never logged
			w.WriteHeader(http.StatusNoContent)
		})
	a.handle("GET /api/v1/secrets", "List secret names (values never returned)",
		"admin:secrets", nil, []string{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			names, err := a.Store.ListSecretNames(r.Context(), a.tenantOf(r, p))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			if names == nil {
				names = []string{}
			}
			a.writeJSON(w, http.StatusOK, names)
		})
	a.handle("DELETE /api/v1/secrets/{name}", "Delete secret", "admin:secrets", nil, nil,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			if err := a.Store.DeleteSecret(r.Context(), a.tenantOf(r, p), param(r, "name")); err != nil {
				a.fail(w, r, err)
				return
			}
			a.audit(r, p, "secret.delete", param(r, "name"), nil, nil)
			w.WriteHeader(http.StatusNoContent)
		})

	// audit browser + verification + NDJSON export (SPEC §13.5)
	a.handle("GET /api/v1/audit", "Search audit log", "admin:audit", nil, listResponse{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			q := r.URL.Query()
			f := storage.AuditFilter{
				TenantID: a.tenantOf(r, p), ActorID: q.Get("actorId"),
				ActorType: q.Get("actorType"), Action: q.Get("action"),
				Resource: q.Get("resource"), Limit: queryInt(r, "limit", 200),
			}
			if v := q.Get("afterSeq"); v != "" {
				f.AfterSeq = int64(queryInt(r, "afterSeq", 0))
			}
			entries, err := a.Store.QueryAudit(r.Context(), f)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			a.writeList(w, entries, "")
		})

	a.handle("GET /api/v1/audit:export", "NDJSON audit export (SIEM)", "admin:audit", nil, nil,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			w.Header().Set("Content-Type", "application/x-ndjson")
			enc := json.NewEncoder(w)
			var afterSeq int64
			for {
				entries, err := a.Store.QueryAudit(r.Context(), storage.AuditFilter{
					TenantID: a.tenantOf(r, p), AfterSeq: afterSeq, Limit: 1000, Asc: true})
				if err != nil || len(entries) == 0 {
					return
				}
				for _, e := range entries {
					_ = enc.Encode(e)
					afterSeq = e.Seq
				}
				if len(entries) < 1000 {
					return
				}
			}
		})

	a.handle("POST /api/v1/audit:verify", "Verify the audit hash chain", "admin:audit", nil, nil,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			n, err := a.Store.VerifyAudit(r.Context())
			if err != nil {
				a.writeJSON(w, http.StatusOK, map[string]any{
					"intact": false, "verified": n, "error": err.Error()})
				return
			}
			a.writeJSON(w, http.StatusOK, map[string]any{"intact": true, "verified": n})
		})

	// GDPR data export for a contact (SPEC §13.4 / A-15.04)
	a.handle("GET /api/v1/contacts/{name}:data-export", "GDPR data disclosure for a contact",
		"admin:audit", nil, nil,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			tenant := a.tenantOf(r, p)
			contact, err := storage.LoadOne[model.Contact](r.Context(), a.Store, tenant,
				storage.KindContact, param(r, "name"))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			// notifications referencing the contact
			events, _ := a.Store.QueryEvents(r.Context(), storage.EventFilter{
				TenantID: tenant, Types: []string{string(model.EventNotification)}, Limit: 1000})
			var notifications []*model.Event
			for _, e := range events {
				if strings.Contains(string(e.Payload), contact.ID) ||
					strings.Contains(string(e.Payload), contact.Name) {
					notifications = append(notifications, e)
				}
			}
			auditRows, _ := a.Store.QueryAudit(r.Context(), storage.AuditFilter{
				TenantID: tenant, ActorID: contact.Name, Limit: 1000})
			a.audit(r, p, "contact.data-export", contact.Name, nil, nil)
			w.Header().Set("Content-Disposition",
				`attachment; filename="data-export-`+contact.Name+`.json"`)
			a.writeJSON(w, http.StatusOK, map[string]any{
				"contact": contact, "notifications": notifications, "auditEntries": auditRows,
				"generatedAt": time.Now().UTC(), "format": "northplane-gdpr-export/1",
			})
		})

	// DLQ surface (F-04.04)
	a.handle("GET /api/v1/notifications/dead-letters", "Dead-letter queue", "alerts:read", nil, listResponse{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			items, err := a.Store.DeadLetters(r.Context(), a.tenantOf(r, p), queryInt(r, "limit", 100))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			a.writeList(w, items, "")
		})
	a.handle("POST /api/v1/notifications/dead-letters/{id}:replay", "Requeue a dead letter",
		"alerts:ack", nil, nil,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			if err := a.Store.ReplayDeadLetter(r.Context(), a.tenantOf(r, p), param(r, "id")); err != nil {
				a.fail(w, r, err)
				return
			}
			a.audit(r, p, "dlq.replay", param(r, "id"), nil, nil)
			w.WriteHeader(http.StatusAccepted)
		}).Status(http.StatusAccepted)

	// Web Push subscription registration (ADR-12)
	type pushSub struct {
		Endpoint string          `json:"endpoint"`
		Keys     json.RawMessage `json:"keys"`
	}
	a.handle("POST /api/v1/push-subscriptions", "Register Web Push subscription", "", pushSub{}, nil,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			if p == nil || p.ActorType != model.ActorUser {
				a.problem(w, r, http.StatusUnauthorized, "np:auth/required", "login required", "")
				return
			}
			var req pushSub
			if !a.decode(w, r, &req) {
				return
			}
			if req.Endpoint == "" {
				a.validationError(w, r, "endpoint", "endpoint required")
				return
			}
			_, err := a.Store.DB().ExecContext(r.Context(), a.Store.Q(
				`INSERT INTO push_subscriptions (id, user_id, endpoint, keys, created_at)
				 VALUES (?,?,?,?,?)`),
				model.NewID(), p.ActorID, req.Endpoint, string(req.Keys),
				a.Store.T(time.Now().UTC()))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			w.WriteHeader(http.StatusCreated)
		})
}

// minPasswordLen mirrors the first-run /setup minimum (web.minSetupPasswordLen):
// local passwords are ≥ 12 characters everywhere they are set (SPEC §13.2).
const minPasswordLen = 12

// adminRole is the built-in role the last-admin guard protects.
const adminRole = "admin"

// hasRole reports membership in a role-name slice.
func hasRole(roles []string, name string) bool {
	for _, r := range roles {
		if r == name {
			return true
		}
	}
	return false
}

// registerUsers wires the user-management surface (SPEC §11.2 / §13.2).
// All mutations require admin:users and are audited; password hashes are
// never returned (model.User.PassHash has json:"-"). The last enabled
// local admin cannot be deleted, disabled or stripped of the admin role —
// that 409 (np:users/last-admin) keeps an install administrable.
func (a *API) registerUsers() {
	a.handle("GET /api/v1/users", "List users", "admin:users", nil, listResponse{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			users, err := a.Store.ListUsers(r.Context())
			if err != nil {
				a.fail(w, r, err)
				return
			}
			a.writeList(w, users, "")
		})

	a.handle("GET /api/v1/users/{id}", "Get a user", "admin:users", nil, model.User{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			user, err := a.Store.GetUser(r.Context(), param(r, "id"))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			a.writeJSON(w, http.StatusOK, user) // PassHash elided by json:"-"
		})

	// Create a local user. password is optional: omit it and the account
	// can only authenticate via OIDC until an admin sets one (:set-password).
	type createUser struct {
		Name     string   `json:"name"`
		Email    string   `json:"email"`
		Password string   `json:"password,omitempty"`
		Roles    []string `json:"roles,omitempty"`
		Disabled bool     `json:"disabled,omitempty"`
	}
	a.handle("POST /api/v1/users", "Create a local user", "admin:users", createUser{}, model.User{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			var req createUser
			if !a.decode(w, r, &req) {
				return
			}
			req.Name, req.Email = strings.TrimSpace(req.Name), strings.TrimSpace(req.Email)
			if req.Name == "" || req.Email == "" {
				a.validationError(w, r, "user", "name and email required")
				return
			}
			var hash string
			if req.Password != "" {
				if len([]rune(req.Password)) < minPasswordLen {
					a.validationError(w, r, "password", "password must be at least 12 characters")
					return
				}
				hash = auth.HashSecret(req.Password)
			}
			user, err := a.Store.CreateUser(r.Context(), &model.User{
				Name: req.Name, Email: req.Email, Local: true,
				// Scope the new account to the operator's active tenant: a
				// central admin (admin:tenants) creating a user under
				// X-Northplane-Tenant provisions a customer login for THAT
				// tenant; otherwise it lands in the operator's own tenant.
				TenantID: a.tenantOf(r, p),
				PassHash: hash, Roles: req.Roles, Disabled: req.Disabled,
			})
			if err != nil {
				a.failUser(w, r, err) // ErrDuplicate → friendly 409 email-in-use
				return
			}
			a.audit(r, p, "user.create", user.ID, nil, map[string]any{
				"name": user.Name, "email": user.Email, "roles": user.Roles, "disabled": user.Disabled})
			a.writeJSON(w, http.StatusCreated, user)
		})

	// Update name/email/roles/disabled. Absent fields (nil) are left as-is.
	type updateUser struct {
		Name     *string   `json:"name,omitempty"`
		Email    *string   `json:"email,omitempty"`
		Roles    *[]string `json:"roles,omitempty"`
		Disabled *bool     `json:"disabled,omitempty"`
	}
	a.handle("PUT /api/v1/users/{id}", "Update a user", "admin:users", updateUser{}, model.User{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			var req updateUser
			if !a.decode(w, r, &req) {
				return
			}
			before, err := a.Store.GetUser(r.Context(), param(r, "id"))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			after := *before
			if req.Name != nil {
				after.Name = strings.TrimSpace(*req.Name)
			}
			if req.Email != nil {
				after.Email = strings.TrimSpace(*req.Email)
			}
			if req.Roles != nil {
				after.Roles = *req.Roles
			}
			if req.Disabled != nil {
				after.Disabled = *req.Disabled
			}
			if after.Name == "" || after.Email == "" {
				a.validationError(w, r, "user", "name and email must not be empty")
				return
			}
			// Last-admin guard: refuse a change that strips the final enabled
			// local admin of its admin status (disable or de-role).
			lostAdmin := before.Local && !before.Disabled && hasRole(before.Roles, adminRole) &&
				(after.Disabled || !hasRole(after.Roles, adminRole))
			if lostAdmin && a.wouldOrphanAdmins(w, r) {
				return
			}
			if err := a.Store.UpdateUser(r.Context(), &after); err != nil {
				a.failUser(w, r, err)
				return
			}
			updated, _ := a.Store.GetUser(r.Context(), after.ID)
			a.audit(r, p, "user.update", after.ID,
				map[string]any{"name": before.Name, "email": before.Email,
					"roles": before.Roles, "disabled": before.Disabled},
				map[string]any{"name": after.Name, "email": after.Email,
					"roles": after.Roles, "disabled": after.Disabled})
			a.writeJSON(w, http.StatusOK, updated)
		})

	// Admin password reset. An empty password clears it (OIDC-only login).
	type setPassword struct {
		Password string `json:"password"`
	}
	a.handle("POST /api/v1/users/{id}:set-password", "Set a user's password (admin reset)",
		"admin:users", setPassword{}, nil,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			var req setPassword
			if !a.decode(w, r, &req) {
				return
			}
			if _, err := a.Store.GetUser(r.Context(), param(r, "id")); err != nil {
				a.fail(w, r, err)
				return
			}
			var hash string
			if req.Password != "" {
				if len([]rune(req.Password)) < minPasswordLen {
					a.validationError(w, r, "password", "password must be at least 12 characters")
					return
				}
				hash = auth.HashSecret(req.Password)
			}
			if err := a.Store.SetPassword(r.Context(), param(r, "id"), hash); err != nil {
				a.fail(w, r, err)
				return
			}
			a.audit(r, p, "user.set-password", param(r, "id"), nil, nil) // password never logged
			w.WriteHeader(http.StatusNoContent)
		})

	// Self-service password change for the logged-in user principal.
	type changePassword struct {
		OldPassword string `json:"oldPassword"`
		NewPassword string `json:"newPassword"`
	}
	a.handle("POST /api/v1/users/me:change-password", "Change your own password",
		"", changePassword{}, nil,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			if p == nil || p.ActorType != model.ActorUser {
				a.problem(w, r, http.StatusUnauthorized, "np:auth/required", "login required", "")
				return
			}
			var req changePassword
			if !a.decode(w, r, &req) {
				return
			}
			user, err := a.Store.GetUser(r.Context(), p.ActorID)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			// Verify the current password. An account without a local
			// password (OIDC-only) cannot self-serve a change here.
			if user.PassHash == "" || !auth.VerifySecret(req.OldPassword, user.PassHash) {
				a.problem(w, r, http.StatusForbidden, "np:auth/bad-password",
					"current password is incorrect", "")
				return
			}
			if len([]rune(req.NewPassword)) < minPasswordLen {
				a.validationError(w, r, "password", "password must be at least 12 characters")
				return
			}
			if err := a.Store.SetPassword(r.Context(), user.ID, auth.HashSecret(req.NewPassword)); err != nil {
				a.fail(w, r, err)
				return
			}
			a.audit(r, p, "user.change-password", user.ID, nil, nil)
			w.WriteHeader(http.StatusNoContent)
		})

	a.handle("DELETE /api/v1/users/{id}", "Delete a user", "admin:users", nil, nil,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			before, err := a.Store.GetUser(r.Context(), param(r, "id"))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			if before.Local && !before.Disabled && hasRole(before.Roles, adminRole) &&
				a.wouldOrphanAdmins(w, r) {
				return
			}
			if err := a.Store.DeleteUser(r.Context(), before.ID); err != nil {
				a.fail(w, r, err)
				return
			}
			a.audit(r, p, "user.delete", before.ID,
				map[string]any{"name": before.Name, "email": before.Email}, nil)
			w.WriteHeader(http.StatusNoContent)
		})

	a.registerPreferences()
}

// registerPreferences wires per-actor UI settings (P1 parity: every knob
// the UI offers is equally settable via API/MCP — SPEC §12). "me" (or the
// caller's own actor ID) needs no extra permission; reading or writing
// someone else's preferences is an admin:users operation so an agent can
// administer them.
func (a *API) registerPreferences() {
	// resolve maps {id} to the target actor ID, enforcing the self-vs-admin
	// permission split. Returns "" after writing the error response.
	resolve := func(w http.ResponseWriter, r *http.Request, p *auth.Principal) string {
		if p == nil {
			a.problem(w, r, http.StatusUnauthorized, "np:auth/required",
				"authentication required", "")
			return ""
		}
		id := param(r, "id")
		if id == "me" || id == p.ActorID {
			return p.ActorID
		}
		if !p.Allow("admin:users") {
			a.problem(w, r, http.StatusForbidden, "np:auth/forbidden",
				"missing permission", "admin:users")
			return ""
		}
		return id
	}

	a.handle("GET /api/v1/users/{id}/preferences", "Get a user's UI preferences ({id} may be \"me\")",
		"", nil, model.Preferences{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			id := resolve(w, r, p)
			if id == "" {
				return
			}
			prefs, err := storage.LoadOne[model.Preferences](r.Context(), a.Store,
				a.tenantOf(r, p), storage.KindPreference, id)
			if errors.Is(err, storage.ErrNotFound) {
				prefs = &model.Preferences{} // unset → defaults
			} else if err != nil {
				a.fail(w, r, err)
				return
			}
			a.writeJSON(w, http.StatusOK, prefs)
		})

	a.handle("PUT /api/v1/users/{id}/preferences", "Set a user's UI preferences ({id} may be \"me\")",
		"", model.Preferences{}, model.Preferences{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			id := resolve(w, r, p)
			if id == "" {
				return
			}
			var req model.Preferences
			if !a.decode(w, r, &req) {
				return
			}
			if v := req.RefreshIntervalMs; v != nil && *v != 0 && (*v < 1000 || *v > 24*60*60*1000) {
				a.validationError(w, r, "preferences",
					"refreshIntervalMs must be 0 (off) or between 1000 and 86400000")
				return
			}
			tenant := a.tenantOf(r, p)
			before, _ := storage.LoadOne[model.Preferences](r.Context(), a.Store,
				tenant, storage.KindPreference, id)
			if _, err := a.Store.PutResource(r.Context(), tenant,
				storage.KindPreference, id, req, 0); err != nil {
				a.fail(w, r, err)
				return
			}
			a.audit(r, p, "preferences.update", id, before, req)
			a.writeJSON(w, http.StatusOK, req)
		})
}

// wouldOrphanAdmins reports (and answers 409 np:users/last-admin) when the
// install currently has only one enabled local admin — i.e. removing that
// one would leave none. Callers gate on "is the target the last admin?"
// before invoking, so a count of ≤ 1 means this very change is the orphaning
// one. Fails closed (treats a count error as "would orphan").
func (a *API) wouldOrphanAdmins(w http.ResponseWriter, r *http.Request) bool {
	n, err := a.Store.CountEnabledAdmins(r.Context())
	if err != nil || n <= 1 {
		a.problem(w, r, http.StatusConflict, "np:users/last-admin",
			"cannot remove the last enabled local administrator", "")
		return true
	}
	return false
}

// failUser maps the email-uniqueness conflict onto a user-friendly 409
// before delegating the rest to fail (404/422/500 as usual).
func (a *API) failUser(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, storage.ErrDuplicate) {
		a.problem(w, r, http.StatusConflict, "np:users/email-in-use",
			"a user with this email already exists", "")
		return
	}
	a.fail(w, r, err)
}
