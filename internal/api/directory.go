package api

import (
	"net/http"

	"github.com/northplane/northplane/internal/auth"
	"github.com/northplane/northplane/internal/ldap"
)

// Directory (LDAP) sync surface: status + manual trigger. The sync
// itself runs as a server worker (internal/ldap); these endpoints make
// it observable and forceable from UI/CLI/MCP without waiting for the
// next interval.
func (a *API) registerDirectory() {
	a.handle("GET /api/v1/directory/status", "Directory (LDAP) sync status",
		"admin:users", nil, ldap.Status{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			if a.LDAP == nil {
				a.writeJSON(w, http.StatusOK, ldap.Status{Configured: false})
				return
			}
			a.writeJSON(w, http.StatusOK, a.LDAP.Status())
		})

	a.handle("POST /api/v1/directory:sync", "Run a directory (LDAP) sync now",
		"admin:users", nil, ldap.Result{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			if a.LDAP == nil {
				a.problem(w, r, http.StatusNotImplemented, "np:directory/unconfigured",
					"directory sync not configured", "set the ldap block in config.yaml")
				return
			}
			res, err := a.LDAP.SyncNow(r.Context())
			if err != nil {
				a.problem(w, r, http.StatusBadGateway, "np:directory/sync",
					"directory sync failed", err.Error())
				return
			}
			a.audit(r, p, "directory.sync", "", nil, res)
			a.writeJSON(w, http.StatusOK, res)
		})
}
