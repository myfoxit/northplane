package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"github.com/northplane/northplane/internal/auth"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
)

// Sites: the main-instance half of edge federation (SPEC §7.7 variant
// B). A site document holds the customer-edge config bundle; the edge
// instance authenticates with a tenant-bound token carrying the
// "sites:connect" scope, heartbeats its status and pulls the bundle
// with a conditional GET. Operators manage sites with the ordinary
// config permissions; runtime status lives in kv so heartbeats never
// churn the config document's version.

// siteConnectedWindow: a site is "connected" when its last heartbeat is
// younger than this (edges default to 60s intervals).
const siteConnectedWindow = 5 * time.Minute

func siteStatusKey(tenantID, name string) string {
	return "site_status:" + tenantID + ":" + name
}

// loadSite fetches the site document by name.
func (a *API) loadSite(r *http.Request, tenantID, name string) (*model.Site, int64, error) {
	env, err := a.Store.ResolveResource(r.Context(), tenantID, storage.KindSite, name)
	if err != nil {
		return nil, 0, err
	}
	var site model.Site
	if err := json.Unmarshal(env.Doc, &site); err != nil {
		return nil, 0, err
	}
	return &site, env.Version, nil
}

func bundleETag(bundleYAML string) string {
	sum := sha256.Sum256([]byte(bundleYAML))
	return hex.EncodeToString(sum[:16])
}

func (a *API) registerSites() {
	a.resourceCRUD("sites", storage.KindSite, "config", model.Site{})

	// Overview merges config + runtime status — the sites page payload.
	a.handle("GET /api/v1/sites:overview", "Sites with connection status",
		"objects:read", nil, listResponse{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			tenant := a.tenantOf(r, p)
			envs, err := a.Store.ListResources(r.Context(), tenant, storage.KindSite, "", "", 500)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			views := make([]model.SiteView, 0, len(envs))
			for _, env := range envs {
				var site model.Site
				if err := json.Unmarshal(env.Doc, &site); err != nil {
					continue
				}
				site.Version = env.Version
				view := model.SiteView{Site: site}
				var st model.SiteStatus
				if err := a.Store.KVGet(r.Context(), siteStatusKey(tenant, site.Name), &st); err == nil {
					view.Status = st
					view.Connected = st.LastSeenAt != nil &&
						time.Since(*st.LastSeenAt) < siteConnectedWindow
				}
				views = append(views, view)
			}
			a.writeList(w, views, "")
		})

	// Edge → main: status heartbeat (scope sites:connect).
	a.handle("POST /api/v1/sites/{name}:heartbeat", "Edge status heartbeat",
		"sites:connect", model.SiteHeartbeat{}, nil,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			tenant := a.tenantOf(r, p)
			site, _, err := a.loadSite(r, tenant, param(r, "name"))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			if site.Disabled {
				a.problem(w, r, http.StatusForbidden, "np:sites/disabled",
					"site is disabled", "")
				return
			}
			var hb model.SiteHeartbeat
			if !a.decode(w, r, &hb) {
				return
			}
			now := time.Now().UTC()
			st := model.SiteStatus{
				LastSeenAt: &now, Version: hb.Version, BundleETag: hb.BundleETag,
				ApplyError: hb.ApplyError, Stats: hb.Stats, SourceIP: remoteHost(r),
			}
			if err := a.Store.KVPut(r.Context(), siteStatusKey(tenant, site.Name), st); err != nil {
				a.fail(w, r, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		})

	// Edge ← main: conditional bundle pull (scope sites:connect). The
	// ETag is a content hash, so editing and reverting a bundle does not
	// trigger a needless re-apply.
	a.handle("GET /api/v1/sites/{name}:pull", "Edge config bundle pull (conditional)",
		"sites:connect", nil, nil,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			tenant := a.tenantOf(r, p)
			site, _, err := a.loadSite(r, tenant, param(r, "name"))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			if site.Disabled {
				a.problem(w, r, http.StatusForbidden, "np:sites/disabled",
					"site is disabled", "")
				return
			}
			tag := `"` + bundleETag(site.Bundle) + `"`
			if r.Header.Get("If-None-Match") == tag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", tag)
			w.Header().Set("Content-Type", "application/yaml")
			_, _ = w.Write([]byte(site.Bundle))
		})
}
