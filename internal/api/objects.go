package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/northplane/northplane/internal/alerting"
	"github.com/northplane/northplane/internal/auth"
	"github.com/northplane/northplane/internal/checks"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/selector"
	"github.com/northplane/northplane/internal/storage"
)

func alertingCompile(r *model.AlertRule) (*alerting.CompiledRule, error) {
	return alerting.CompileRule(r)
}

// ObjectBody is the create/update payload for hosts and services.
type ObjectBody struct {
	Name   string           `json:"name"`
	Host   string           `json:"host,omitempty"` // services: host name or id
	Folder string           `json:"folder,omitempty"`
	Labels model.Labels     `json:"labels,omitempty"`
	Spec   model.ObjectSpec `json:"spec"`
}

// ObjectView decorates an object with live state.
type ObjectView struct {
	*model.Object
	HostName string             `json:"hostName,omitempty"`
	State    *model.CheckState  `json:"state,omitempty"`
}

func (a *API) registerObjects() {
	list := func(kind model.Kind) func(http.ResponseWriter, *http.Request, *auth.Principal) {
		return func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			q := r.URL.Query()
			sel, err := selector.Parse(q.Get("selector"))
			if err != nil {
				a.validationError(w, r, "selector", err.Error())
				return
			}
			objs, err := a.Store.ListObjects(r.Context(), storage.ObjectFilter{
				TenantID: a.tenantOf(r, p), Kind: kind, HostID: q.Get("hostId"),
				Folder: q.Get("folder"), Selector: sel, Query: q.Get("q"),
				Cursor: q.Get("cursor"), Limit: queryInt(r, "limit", 200),
			})
			if err != nil {
				a.fail(w, r, err)
				return
			}
			views := a.decorate(r, objs, q.Get("withState") != "false")
			next := ""
			if len(objs) == queryInt(r, "limit", 200) {
				next = objs[len(objs)-1].ID
			}
			a.writeList(w, views, next)
		}
	}
	a.handle("GET /api/v1/objects", "List hosts and services", "objects:read", nil, listResponse{}, list(""))
	a.handle("GET /api/v1/hosts", "List hosts", "objects:read", nil, listResponse{}, list(model.KindHost))
	a.handle("GET /api/v1/services", "List services", "objects:read", nil, listResponse{}, list(model.KindService))

	create := func(kind model.Kind) func(http.ResponseWriter, *http.Request, *auth.Principal) {
		return func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			var body ObjectBody
			if !a.decode(w, r, &body) {
				return
			}
			if body.Name == "" {
				a.validationError(w, r, "name", "name required")
				return
			}
			if !p.AllowFolder(body.Folder) {
				a.problem(w, r, http.StatusForbidden, "np:auth/scope", "folder outside role scope", body.Folder)
				return
			}
			obj := &model.Object{TenantID: a.tenantOf(r, p), Kind: kind, Name: body.Name,
				Folder: body.Folder, Labels: body.Labels, Spec: body.Spec}
			if kind == model.KindService {
				host, err := a.resolveHost(r, p, body.Host)
				if err != nil {
					a.validationError(w, r, "host", err.Error())
					return
				}
				obj.HostID = host.ID
			}
			if err := a.validateSpec(r.Context(), obj); err != nil {
				a.validationError(w, r, "spec", err.Error())
				return
			}
			if err := a.Store.CreateObject(r.Context(), obj); err != nil {
				a.fail(w, r, err)
				return
			}
			a.audit(r, p, string(kind)+".create", obj.ID, nil, obj)
			a.objectChanged(r.Context(), obj)
			etag(w, obj.Version)
			a.writeJSON(w, http.StatusCreated, obj)
		}
	}
	a.handle("POST /api/v1/hosts", "Create host", "objects:write", ObjectBody{}, model.Object{}, create(model.KindHost))
	a.handle("POST /api/v1/services", "Create service", "objects:write", ObjectBody{}, model.Object{}, create(model.KindService))

	a.handle("GET /api/v1/objects/{id}", "Get object with live state", "objects:read", nil, ObjectView{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			obj, err := a.Store.GetObject(r.Context(), a.tenantOf(r, p), param(r, "id"))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			view := a.decorate(r, []*model.Object{obj}, true)[0]
			etag(w, obj.Version)
			a.writeJSON(w, http.StatusOK, view)
		})

	a.handle("PUT /api/v1/objects/{id}", "Update object (If-Match required)", "objects:write", ObjectBody{}, model.Object{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			version, ok := a.requireIfMatch(w, r)
			if !ok {
				return
			}
			var body ObjectBody
			if !a.decode(w, r, &body) {
				return
			}
			obj, err := a.Store.GetObject(r.Context(), a.tenantOf(r, p), param(r, "id"))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			before := *obj
			if body.Name != "" && body.Name != obj.Name {
				a.validationError(w, r, "name", "rename is not supported — recreate the object")
				return
			}
			if body.Folder != "" {
				if !p.AllowFolder(body.Folder) {
					a.problem(w, r, http.StatusForbidden, "np:auth/scope", "folder outside role scope", body.Folder)
					return
				}
				obj.Folder = body.Folder
			}
			if body.Labels != nil {
				obj.Labels = body.Labels
			}
			obj.Spec = body.Spec
			if err := a.validateSpec(r.Context(), obj); err != nil {
				a.validationError(w, r, "spec", err.Error())
				return
			}
			if err := a.Store.UpdateObject(r.Context(), obj, version); err != nil {
				a.fail(w, r, err)
				return
			}
			a.audit(r, p, string(obj.Kind)+".update", obj.ID, before, obj)
			a.objectChanged(r.Context(), obj)
			etag(w, obj.Version)
			a.writeJSON(w, http.StatusOK, obj)
		})

	a.handle("DELETE /api/v1/objects/{id}", "Delete object", "objects:write", nil, nil,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			obj, err := a.Store.GetObject(r.Context(), a.tenantOf(r, p), param(r, "id"))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			if err := a.Store.DeleteObject(r.Context(), obj.TenantID, obj.ID); err != nil {
				a.fail(w, r, err)
				return
			}
			a.audit(r, p, string(obj.Kind)+".delete", obj.ID, obj, nil)
			// objectRemoved deschedules the object AND its cascaded
			// service children (the old path left children on the wheel).
			a.objectRemoved(r.Context(), obj)
			w.WriteHeader(http.StatusNoContent)
		})

	// Effective config: resolved template chain (SPEC §6.2 — kein
	// Rätselraten über Vererbungsergebnisse).
	type effectiveResponse struct {
		Spec  model.ObjectSpec `json:"spec"`
		Chain []string         `json:"templateChain"`
	}
	a.handle("GET /api/v1/objects/{id}/effective-config", "Resolved effective config",
		"objects:read", nil, effectiveResponse{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			entry := a.Catalog.Get(param(r, "id"))
			if entry == nil || entry.Object.TenantID != a.tenantOf(r, p) {
				a.problem(w, r, http.StatusNotFound, "np:not-found", "object not found", "")
				return
			}
			a.writeJSON(w, http.StatusOK, effectiveResponse{Spec: entry.Effective, Chain: entry.Chain})
		})

	a.handle("POST /api/v1/objects/{id}/check-now", "Immediate recheck (priority lane)",
		"checks:run", nil, nil,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			obj, err := a.Store.GetObject(r.Context(), a.tenantOf(r, p), param(r, "id"))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			a.Sched.CheckNow(obj.ID)
			a.audit(r, p, "check.now", obj.ID, nil, nil)
			w.WriteHeader(http.StatusAccepted)
		})

	// Problems view (SPEC §12.3) — priority-sorted hard non-OK states.
	a.handle("GET /api/v1/problems", "Current problems", "objects:read", nil, listResponse{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			rows, err := a.Store.ListProblems(r.Context(), a.tenantOf(r, p),
				r.URL.Query().Get("includeHandled") == "true", queryInt(r, "limit", 500))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			a.writeList(w, rows, "")
		})

	// Batch create (SPEC §11.1: atomic or partial mode — Wizard/Massenanlage).
	type batchRequest struct {
		Mode     string       `json:"mode,omitempty"` // "all-or-nothing" (default) | "partial"
		Hosts    []ObjectBody `json:"hosts,omitempty"`
		Services []ObjectBody `json:"services,omitempty"`
	}
	type batchItemResult struct {
		Name  string `json:"name"`
		ID    string `json:"id,omitempty"`
		Error string `json:"error,omitempty"`
	}
	type batchResponse struct {
		Created int               `json:"created"`
		Failed  int               `json:"failed"`
		Results []batchItemResult `json:"results"`
	}
	a.handle("POST /api/v1/objects:batch", "Bulk create hosts/services", "objects:write",
		batchRequest{}, batchResponse{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			var req batchRequest
			if !a.decode(w, r, &req) {
				return
			}
			atomic := req.Mode == "" || req.Mode == "all-or-nothing"
			resp := batchResponse{}
			tenant := a.tenantOf(r, p)

			tryCreate := func(kind model.Kind, body ObjectBody) batchItemResult {
				obj := &model.Object{TenantID: tenant, Kind: kind, Name: body.Name,
					Folder: body.Folder, Labels: body.Labels, Spec: body.Spec}
				if kind == model.KindService {
					host, err := a.resolveHost(r, p, body.Host)
					if err != nil {
						return batchItemResult{Name: body.Name, Error: err.Error()}
					}
					obj.HostID = host.ID
				}
				if err := a.validateSpec(r.Context(), obj); err != nil {
					return batchItemResult{Name: body.Name, Error: err.Error()}
				}
				if err := a.Store.CreateObject(r.Context(), obj); err != nil {
					return batchItemResult{Name: body.Name, Error: err.Error()}
				}
				return batchItemResult{Name: body.Name, ID: obj.ID}
			}

			if atomic {
				// dry-run validation pass first
				for _, h := range req.Hosts {
					if h.Name == "" {
						a.validationError(w, r, "batch", "host without name")
						return
					}
				}
				for _, s := range req.Services {
					if s.Name == "" || s.Host == "" {
						a.validationError(w, r, "batch", "service requires name and host")
						return
					}
				}
			}
			var created []string
			rollback := func() {
				for _, id := range created {
					_ = a.Store.DeleteObject(r.Context(), tenant, id)
				}
			}
			for _, h := range req.Hosts {
				res := tryCreate(model.KindHost, h)
				resp.Results = append(resp.Results, res)
				if res.Error == "" {
					resp.Created++
					created = append(created, res.ID)
				} else {
					resp.Failed++
					if atomic {
						rollback()
						a.writeJSON(w, http.StatusUnprocessableEntity, resp)
						return
					}
				}
			}
			for _, s := range req.Services {
				res := tryCreate(model.KindService, s)
				resp.Results = append(resp.Results, res)
				if res.Error == "" {
					resp.Created++
					created = append(created, res.ID)
				} else {
					resp.Failed++
					if atomic {
						rollback()
						a.writeJSON(w, http.StatusUnprocessableEntity, resp)
						return
					}
				}
			}
			a.audit(r, p, "objects.batch", "", nil, map[string]int{
				"created": resp.Created, "failed": resp.Failed})
			a.configChanged(r.Context(), tenant)
			a.writeJSON(w, http.StatusOK, resp)
		})

	a.registerConfigResources()
}

// decorate joins live state and host names onto objects. States are
// fetched in one batched query — a 2000-row listing must not issue
// 2000 point lookups (SPEC §11.2 list-endpoint latency).
func (a *API) decorate(r *http.Request, objs []*model.Object, withState bool) []*ObjectView {
	var states map[string]*model.CheckState
	if withState && len(objs) > 0 {
		ids := make([]string, len(objs))
		for i, o := range objs {
			ids[i] = o.ID
		}
		if m, err := a.Store.ListCheckStates(r.Context(), ids); err == nil {
			states = m
		} else {
			a.Log.Error("api: batch state fetch", "err", err)
		}
	}
	views := make([]*ObjectView, 0, len(objs))
	for _, o := range objs {
		v := &ObjectView{Object: o}
		if o.HostID != "" {
			if h := a.Catalog.Get(o.HostID); h != nil {
				v.HostName = h.Object.Name
			}
		}
		if cs, ok := states[o.ID]; ok {
			v.State = cs
		}
		views = append(views, v)
	}
	return views
}

// resolveHost accepts a host name or id.
func (a *API) resolveHost(r *http.Request, p *auth.Principal, ref string) (*model.Object, error) {
	if ref == "" {
		return nil, fmt.Errorf("host required")
	}
	tenant := a.tenantOf(r, p)
	if model.ValidID(ref) {
		if obj, err := a.Store.GetObject(r.Context(), tenant, ref); err == nil {
			return obj, nil
		}
	}
	obj, err := a.Store.GetObjectByName(r.Context(), tenant, model.KindHost, "", ref)
	if err != nil {
		return nil, fmt.Errorf("unknown host %q", ref)
	}
	return obj, nil
}

// validateSpec resolves templates + command references (config errors
// fail fast at write time, SPEC §11.1: 422).
func (a *API) validateSpec(ctx context.Context, obj *model.Object) error {
	eff, _, err := model.EffectiveSpec(obj, func(name string) *model.Template {
		t, err := storage.LoadOne[model.Template](ctx, a.Store, obj.TenantID, storage.KindTemplate, name)
		if err != nil {
			return nil
		}
		return t
	})
	if err != nil {
		return err
	}
	for _, tok := range eff.NotifyOn {
		if !model.ValidNotifyOn[tok] {
			return fmt.Errorf("invalid notifyOn token %q (valid: warning, critical, unknown, down, unreachable, recovery)", tok)
		}
	}
	for _, g := range eff.ContactGroups {
		if _, err := a.Store.ResolveResource(ctx, obj.TenantID, storage.KindContactGroup, g); err != nil {
			return fmt.Errorf("unknown contact group %q", g)
		}
	}
	for _, c := range eff.Contacts {
		if _, err := a.Store.ResolveResource(ctx, obj.TenantID, storage.KindContact, c); err != nil {
			return fmt.Errorf("unknown contact %q", c)
		}
	}
	return nil
}

// registerConfigResources wires templates, check commands, time periods.
func (a *API) registerConfigResources() {
	a.resourceCRUD("templates", storage.KindTemplate, "config", model.Template{})
	a.resourceCRUD("check-commands", storage.KindCheckCommand, "config", model.CheckCommand{})
	a.resourceCRUD("time-periods", storage.KindTimePeriod, "config", model.TimePeriod{})

	// Test console for commands (SPEC §12.3 admin: Check-Commands mit
	// Test-Konsole) — runs a builtin check ad hoc.
	type testCheckRequest struct {
		Builtin string   `json:"builtin"`
		Address string   `json:"address"`
		Args    []string `json:"args,omitempty"`
	}
	type testCheckResponse struct {
		State  int    `json:"state"`
		Label  string `json:"label"`
		Output string `json:"output"`
		Perf   string `json:"perfdata,omitempty"`
		TookMS int64  `json:"tookMs"`
	}
	a.handle("POST /api/v1/check-commands:test", "Run a builtin check ad hoc", "checks:run",
		testCheckRequest{}, testCheckResponse{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			var req testCheckRequest
			if !a.decode(w, r, &req) {
				return
			}
			start := time.Now()
			state, out := checks.Run(r.Context(), req.Builtin,
				checks.Target{Address: req.Address}, req.Args)
			text := out.Text
			if out.Perfdata != "" {
				text = out.Text
			}
			a.writeJSON(w, http.StatusOK, testCheckResponse{
				State: int(state), Label: state.ServiceLabel(), Output: text,
				Perf: out.Perfdata, TookMS: time.Since(start).Milliseconds()})
		})

	a.handle("GET /api/v1/check-commands:builtins", "List builtin check names", "objects:read", nil, []string{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			a.writeJSON(w, http.StatusOK, checks.Names())
		})
}

// resourceCRUD generates the standard CRUD for resource-document kinds.
func (a *API) resourceCRUD(path, kind, permPrefix string, proto any) {
	readPerm := model.Permission(permPrefix + ":read")
	writePerm := model.Permission(permPrefix + ":write")
	if permPrefix == "config" {
		readPerm, writePerm = "objects:read", "config:write"
	}

	a.handle("GET /api/v1/"+path, "List "+path, readPerm, nil, listResponse{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			envs, err := a.Store.ListResources(r.Context(), a.tenantOf(r, p), kind,
				r.URL.Query().Get("q"), r.URL.Query().Get("cursor"), queryInt(r, "limit", 500))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			docs := make([]json.RawMessage, 0, len(envs))
			for _, env := range envs {
				docs = append(docs, env.Doc)
			}
			next := ""
			if len(envs) == queryInt(r, "limit", 500) {
				next = envs[len(envs)-1].Name
			}
			a.writeList(w, docs, next)
		})

	a.handle("POST /api/v1/"+path, "Create "+kind, writePerm, proto, proto,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			var doc map[string]any
			if !a.decode(w, r, &doc) {
				return
			}
			name, _ := doc["name"].(string)
			if name == "" {
				a.validationError(w, r, "name", "name required")
				return
			}
			if err := a.validateResourceDoc(kind, doc); err != nil {
				a.validationError(w, r, kind, err.Error())
				return
			}
			env, err := a.Store.PutResource(r.Context(), a.tenantOf(r, p), kind, name, doc, -1)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			a.audit(r, p, kind+".create", name, nil, json.RawMessage(env.Doc))
			a.configChanged(r.Context(), a.tenantOf(r, p), kind)
			etag(w, env.Version)
			a.writeJSON(w, http.StatusCreated, json.RawMessage(env.Doc))
		})

	a.handle("GET /api/v1/"+path+"/{name}", "Get "+kind, readPerm, nil, proto,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			env, err := a.Store.ResolveResource(r.Context(), a.tenantOf(r, p), kind, param(r, "name"))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			etag(w, env.Version)
			a.writeJSON(w, http.StatusOK, json.RawMessage(env.Doc))
		})

	a.handle("PUT /api/v1/"+path+"/{name}", "Update "+kind+" (If-Match)", writePerm, proto, proto,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			version, ok := a.requireIfMatch(w, r)
			if !ok {
				return
			}
			var doc map[string]any
			if !a.decode(w, r, &doc) {
				return
			}
			if err := a.validateResourceDoc(kind, doc); err != nil {
				a.validationError(w, r, kind, err.Error())
				return
			}
			name := param(r, "name")
			old, _ := a.Store.GetResource(r.Context(), a.tenantOf(r, p), kind, name)
			env, err := a.Store.PutResource(r.Context(), a.tenantOf(r, p), kind, name, doc, version)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			var beforeDoc json.RawMessage
			if old != nil {
				beforeDoc = old.Doc
			}
			a.audit(r, p, kind+".update", name, beforeDoc, json.RawMessage(env.Doc))
			a.configChanged(r.Context(), a.tenantOf(r, p), kind)
			etag(w, env.Version)
			a.writeJSON(w, http.StatusOK, json.RawMessage(env.Doc))
		})

	a.handle("DELETE /api/v1/"+path+"/{name}", "Delete "+kind, writePerm, nil, nil,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			name := param(r, "name")
			old, _ := a.Store.GetResource(r.Context(), a.tenantOf(r, p), kind, name)
			if err := a.Store.DeleteResource(r.Context(), a.tenantOf(r, p), kind, name); err != nil {
				a.fail(w, r, err)
				return
			}
			var beforeDoc json.RawMessage
			if old != nil {
				beforeDoc = old.Doc
			}
			a.audit(r, p, kind+".delete", name, beforeDoc, nil)
			a.configChanged(r.Context(), a.tenantOf(r, p), kind)
			w.WriteHeader(http.StatusNoContent)
		})
}

// validateResourceDoc applies kind-specific validation before storage.
func (a *API) validateResourceDoc(kind string, doc map[string]any) error {
	raw, _ := json.Marshal(doc)
	switch kind {
	case storage.KindAlertRule:
		var rule model.AlertRule
		if err := json.Unmarshal(raw, &rule); err != nil {
			return err
		}
		if name, _ := doc["name"].(string); name != "" {
			rule.Name = name
		}
		_, err := alertingCompile(&rule)
		return err
	case storage.KindEscalationPolicy:
		var pol model.EscalationPolicy
		if err := json.Unmarshal(raw, &pol); err != nil {
			return err
		}
		if len(pol.Steps) == 0 {
			return fmt.Errorf("policy needs at least one step")
		}
		return nil
	case storage.KindChannel:
		var ch model.NotificationChannel
		if err := json.Unmarshal(raw, &ch); err != nil {
			return err
		}
		if ch.Type == "" {
			return fmt.Errorf("channel type required")
		}
		return nil
	case storage.KindSchedule:
		var s model.Schedule
		if err := json.Unmarshal(raw, &s); err != nil {
			return err
		}
		for _, l := range s.Layers {
			if len(l.Participants) == 0 {
				return fmt.Errorf("rotation without participants")
			}
		}
		return nil
	}
	return nil
}
