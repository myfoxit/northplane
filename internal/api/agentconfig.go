package api

import (
	"net/http"
	"sort"

	"github.com/northplane/northplane/internal/auth"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/nagios"
)

// Agent check config (SPEC §8.4): np-agent pulls the list of
// agent-class checks defined for its host, so the daemon stays thin and
// is configured centrally — services with checkCommand "agent:exec:…"
// are executed by the agent as ordinary Nagios plugins and submitted as
// passive results. Standard macros ($HOSTADDRESS$, $ARGn$, custom vars)
// are expanded server-side; $SECRET refs are deliberately NOT resolved
// here (secrets never leave the server via config pulls — put them in
// the agent's local environment instead).

// AgentCheck is one centrally managed plugin execution.
type AgentCheck struct {
	Service         string   `json:"service"`
	Command         string   `json:"command"`
	Args            []string `json:"args,omitempty"`
	IntervalSeconds int      `json:"intervalSeconds,omitempty"`
	TimeoutSeconds  int      `json:"timeoutSeconds,omitempty"`
}

// AgentChecksResponse is the pull payload.
type AgentChecksResponse struct {
	Host   string       `json:"host"`
	Checks []AgentCheck `json:"checks"`
}

func (a *API) registerAgentConfig() {
	a.handle("GET /api/v1/agent/checks", "Agent-class checks for a host (np-agent pull)",
		"objects:read", nil, AgentChecksResponse{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			hostName := r.URL.Query().Get("host")
			if hostName == "" {
				a.validationError(w, r, "host", "query parameter host required")
				return
			}
			tenant := a.tenantOf(r, p)
			host, err := a.Store.GetObjectByName(r.Context(), tenant, model.KindHost, "", hostName)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			resp := AgentChecksResponse{Host: host.Name, Checks: []AgentCheck{}}
			for _, e := range a.Catalog.All() {
				if e.Object.TenantID != tenant || e.Object.HostID != host.ID ||
					e.Class != model.CommandAgent || len(e.Argv) == 0 {
					continue
				}
				mc := &nagios.MacroContext{
					Host: host, Service: e.Object,
					ServiceSpec: &e.Effective, Args: e.MacroArgs,
				}
				if he := a.Catalog.Get(host.ID); he != nil {
					mc.HostSpec = &he.Effective
				}
				argv, _ := mc.ExpandArgs(e.Argv)
				check := AgentCheck{Service: e.Object.Name, Command: argv[0], Args: argv[1:]}
				if d := e.Effective.Interval.D(); d > 0 {
					check.IntervalSeconds = int(d.Seconds())
				}
				if d := e.Effective.Timeout.D(); d > 0 {
					check.TimeoutSeconds = int(d.Seconds())
				}
				resp.Checks = append(resp.Checks, check)
			}
			sort.Slice(resp.Checks, func(i, j int) bool {
				return resp.Checks[i].Service < resp.Checks[j].Service
			})
			a.writeJSON(w, http.StatusOK, resp)
		})
}
