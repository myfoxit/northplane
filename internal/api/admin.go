package api

import (
	"encoding/json"
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

	// users
	a.handle("GET /api/v1/users", "List users", "admin:users", nil, listResponse{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			users, err := a.Store.ListUsers(r.Context())
			if err != nil {
				a.fail(w, r, err)
				return
			}
			a.writeList(w, users, "")
		})

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
		Token string          `json:"token"` // shown once
		Meta  model.APIToken  `json:"meta"`
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
		})

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
