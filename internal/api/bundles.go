package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/northplane/northplane/internal/auth"
	"github.com/northplane/northplane/internal/bundle"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/selector"
	"github.com/northplane/northplane/internal/storage"
)

// Bundle plan/apply (SPEC §11.6): server-side two-step with apply
// tokens — the same mechanism the AI layer uses for propose/apply
// (SPEC §10.1). Plans are cached briefly; the token proves the diff the
// caller approved is the diff that runs.

// PlanAction is one step of a plan.
type PlanAction struct {
	Action string         `json:"action"` // create|update|delete
	Kind   string         `json:"kind"`
	Name   string         `json:"name"`
	Host   string         `json:"host,omitempty"`
	Diff   map[string]any `json:"diff,omitempty"` // field → [old, new]
}

// PlanResult is the dry-run response.
type PlanResult struct {
	Plan       []PlanAction `json:"plan"`
	Warnings   []string     `json:"warnings,omitempty"`
	ApplyToken string       `json:"applyToken,omitempty"`
}

type cachedPlan struct {
	tenantID string
	docs     []bundle.Doc
	prune    bool
	pruneSel string
	hash     string
	expires  time.Time
}

var planCache sync.Map // token → *cachedPlan

const kindResourceMap = "kindmap"

// bundleKindToStorage maps bundle kinds to resource kinds.
var bundleKindToStorage = map[string]string{
	"Template":            storage.KindTemplate,
	"CheckCommand":        storage.KindCheckCommand,
	"TimePeriod":          storage.KindTimePeriod,
	"AlertRule":           storage.KindAlertRule,
	"AlertGroup":          storage.KindAlertGroup,
	"EscalationPolicy":    storage.KindEscalationPolicy,
	"Schedule":            storage.KindSchedule,
	"Contact":             storage.KindContact,
	"ContactGroup":        storage.KindContactGroup,
	"Channel":             storage.KindChannel,
	"EventSource":         storage.KindEventSource,
	"BusinessService":     storage.KindBusinessService,
	"Dashboard":           storage.KindDashboard,
	"Report":              storage.KindReport,
	"StaticGroup":         storage.KindStaticGroup,
	"WebhookSubscription": storage.KindWebhookSub,
	"SavedFilter":         storage.KindSavedFilter,
	"Role":                storage.KindRole,
	"IVRMenu":             storage.KindIVRMenu,
}

func (a *API) registerBundles() {
	// plan: dry-run diff (also reachable as :apply?dryRun=true)
	planHandler := func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		docs, ok := a.readBundle(w, r)
		if !ok {
			return
		}
		prune := r.URL.Query().Get("prune") == "true"
		pruneSel := r.URL.Query().Get("selector")
		plan, warnings, err := a.computePlan(r.Context(), a.tenantOf(r, p), docs, prune, pruneSel)
		if err != nil {
			a.validationError(w, r, "bundle", err.Error())
			return
		}
		res := PlanResult{Plan: plan, Warnings: warnings}
		if len(plan) > 0 {
			token := "ap_" + model.NewSecret(16)
			raw, _ := json.Marshal(plan)
			sum := sha256.Sum256(raw)
			planCache.Store(token, &cachedPlan{
				tenantID: a.tenantOf(r, p), docs: docs, prune: prune, pruneSel: pruneSel,
				hash: hex.EncodeToString(sum[:]), expires: time.Now().Add(10 * time.Minute),
			})
			res.ApplyToken = token
		}
		a.writeJSON(w, http.StatusOK, res)
	}
	a.handle("POST /api/v1/config/bundles:plan", "Dry-run diff of a bundle", "objects:read", nil, PlanResult{}, planHandler)

	a.handle("POST /api/v1/config/bundles:apply", "Apply a bundle (dryRun=true plans)",
		"config:write", nil, PlanResult{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			if r.URL.Query().Get("dryRun") == "true" {
				planHandler(w, r, p)
				return
			}
			tenant := a.tenantOf(r, p)
			// Path 1: apply a previously planned token
			if token := r.URL.Query().Get("applyToken"); token != "" {
				v, ok := planCache.LoadAndDelete(token)
				if !ok {
					a.problem(w, r, http.StatusConflict, "np:bundle/token",
						"apply token unknown or expired — re-plan", "")
					return
				}
				plan := v.(*cachedPlan)
				if plan.tenantID != tenant || time.Now().After(plan.expires) {
					a.problem(w, r, http.StatusConflict, "np:bundle/token",
						"apply token expired — re-plan", "")
					return
				}
				a.applyBundle(w, r, p, plan.docs, plan.prune, plan.pruneSel)
				return
			}
			// Path 2: direct apply of the posted bundle
			docs, ok := a.readBundle(w, r)
			if !ok {
				return
			}
			a.applyBundle(w, r, p, docs,
				r.URL.Query().Get("prune") == "true", r.URL.Query().Get("selector"))
		})

	// export: canonical bundle (SPEC §11.6 round-trip)
	a.handle("GET /api/v1/config/bundles:export", "Export configuration as bundle YAML",
		"objects:read", nil, nil,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			docs, err := a.exportBundle(r.Context(), a.tenantOf(r, p), r.URL.Query().Get("folder"))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			out, err := bundle.Render(docs)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			w.Header().Set("Content-Type", "application/yaml")
			_, _ = w.Write(out)
		})
}

// PlanBundleYAML implements the ai.BundlePlanner hook: dry-run a YAML
// bundle for the propose_config_change tool (SPEC §10.3).
func (a *API) PlanBundleYAML(ctx context.Context, tenantID, yamlText string) (any, error) {
	docs, err := bundle.ParseBytes([]byte(yamlText))
	if err != nil {
		return nil, err
	}
	if errs := bundle.Validate(docs); len(errs) > 0 {
		return nil, fmt.Errorf("bundle invalid: %s", strings.Join(errs, "; "))
	}
	plan, warnings, err := a.computePlan(ctx, tenantID, docs, false, "")
	if err != nil {
		return nil, err
	}
	res := PlanResult{Plan: plan, Warnings: warnings}
	if len(plan) > 0 {
		token := "ap_" + model.NewSecret(16)
		raw, _ := json.Marshal(plan)
		sum := sha256.Sum256(raw)
		planCache.Store(token, &cachedPlan{
			tenantID: tenantID, docs: docs,
			hash: hex.EncodeToString(sum[:]), expires: time.Now().Add(10 * time.Minute),
		})
		res.ApplyToken = token
	}
	return res, nil
}

// ApplyBundleYAML implements the ai.BundlePlanner apply hook: the
// apply_config_change MCP tool routes through the approval queue, so by
// the time this runs a human has approved the diff (SPEC §10.3).
func (a *API) ApplyBundleYAML(ctx context.Context, tenantID, yamlText string) (any, error) {
	docs, err := bundle.ParseBytes([]byte(yamlText))
	if err != nil {
		return nil, err
	}
	if errs := bundle.Validate(docs); len(errs) > 0 {
		return nil, fmt.Errorf("bundle invalid: %s", strings.Join(errs, "; "))
	}
	_, warnings, err := a.computePlan(ctx, tenantID, docs, false, "")
	if err != nil {
		return nil, err
	}
	ordered := make([]bundle.Doc, len(docs))
	copy(ordered, docs)
	sort.SliceStable(ordered, func(i, j int) bool {
		return kindRankOf(ordered[i].Kind) < kindRankOf(ordered[j].Kind)
	})
	applied := []PlanAction{}
	for _, doc := range ordered {
		act, err := a.applyDoc(ctx, tenantID, doc)
		if err != nil {
			return nil, fmt.Errorf("apply failed at %s: %w", doc.Ident(), err)
		}
		if act != "" {
			applied = append(applied, PlanAction{Action: act, Kind: doc.Kind,
				Name: doc.Metadata.Name, Host: doc.Metadata.Host})
		}
	}
	// Bundles touch objects AND rules: "object" forces the catalog
	// reload, the rule kind recompiles the alerting engine.
	a.configChanged(ctx, tenantID, "object", storage.KindAlertRule)
	return PlanResult{Plan: applied, Warnings: warnings}, nil
}

func (a *API) readBundle(w http.ResponseWriter, r *http.Request) ([]bundle.Doc, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<20))
	if err != nil {
		a.problem(w, r, http.StatusRequestEntityTooLarge, "np:bundle/size", "bundle too large", "")
		return nil, false
	}
	docs, err := bundle.ParseBytes(body)
	if err != nil {
		a.validationError(w, r, "bundle", err.Error())
		return nil, false
	}
	if errs := bundle.Validate(docs); len(errs) > 0 {
		a.validationError(w, r, "bundle", strings.Join(errs, "; "))
		return nil, false
	}
	return docs, true
}

// computePlan diffs desired docs against current state.
func (a *API) computePlan(ctx context.Context, tenantID string, docs []bundle.Doc,
	prune bool, pruneSel string) ([]PlanAction, []string, error) {
	var plan []PlanAction
	var warnings []string
	desired := map[string]bool{}

	for _, doc := range docs {
		desired[doc.Ident()] = true
		switch doc.Kind {
		case "Host", "Service":
			action, diff, err := a.planObject(ctx, tenantID, doc)
			if err != nil {
				return nil, nil, fmt.Errorf("%s: %w", doc.Ident(), err)
			}
			if action != "" {
				plan = append(plan, PlanAction{Action: action, Kind: doc.Kind,
					Name: doc.Metadata.Name, Host: doc.Metadata.Host, Diff: diff})
			}
		default:
			kind, ok := bundleKindToStorage[doc.Kind]
			if !ok {
				warnings = append(warnings, "unsupported kind "+doc.Kind)
				continue
			}
			current, err := a.Store.GetResource(ctx, tenantID, kind, doc.Metadata.Name)
			if err == storage.ErrNotFound {
				plan = append(plan, PlanAction{Action: "create", Kind: doc.Kind, Name: doc.Metadata.Name})
				continue
			}
			if err != nil {
				return nil, nil, err
			}
			diff := diffDocs(current.Doc, doc)
			if len(diff) > 0 {
				plan = append(plan, PlanAction{Action: "update", Kind: doc.Kind,
					Name: doc.Metadata.Name, Diff: diff})
			}
		}
	}

	if prune {
		currentDocs, err := a.exportBundle(ctx, tenantID, "")
		if err != nil {
			return nil, nil, err
		}
		var sel selector.Selector
		if pruneSel != "" {
			sel, err = selector.Parse(pruneSel)
			if err != nil {
				return nil, nil, err
			}
		}
		for _, cur := range currentDocs {
			if desired[cur.Ident()] {
				continue
			}
			if pruneSel != "" && !sel.Matches(cur.Metadata.Labels) {
				continue
			}
			plan = append(plan, PlanAction{Action: "delete", Kind: cur.Kind,
				Name: cur.Metadata.Name, Host: cur.Metadata.Host})
		}
	}
	sort.SliceStable(plan, func(i, j int) bool {
		rank := map[string]int{"delete": 2, "update": 1, "create": 0}
		return rank[plan[i].Action] < rank[plan[j].Action]
	})
	return plan, warnings, nil
}

// planObject diffs one Host/Service doc.
func (a *API) planObject(ctx context.Context, tenantID string, doc bundle.Doc) (string, map[string]any, error) {
	spec, err := docToSpec(doc)
	if err != nil {
		return "", nil, err
	}
	kind := model.KindHost
	hostID := ""
	if doc.Kind == "Service" {
		kind = model.KindService
		host, err := a.Store.GetObjectByName(ctx, tenantID, model.KindHost, "", doc.Metadata.Host)
		if err == storage.ErrNotFound {
			// host may be created earlier in this bundle → assume create
			return "create", nil, nil
		}
		if err != nil {
			return "", nil, err
		}
		hostID = host.ID
	}
	current, err := a.Store.GetObjectByName(ctx, tenantID, kind, hostID, doc.Metadata.Name)
	if err == storage.ErrNotFound {
		return "create", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	diff := map[string]any{}
	if orSlashS(doc.Metadata.Folder) != current.Folder {
		diff["folder"] = []any{current.Folder, orSlashS(doc.Metadata.Folder)}
	}
	if !reflect.DeepEqual(model.Labels(doc.Metadata.Labels), orEmptyLabels(current.Labels)) {
		diff["labels"] = []any{current.Labels, doc.Metadata.Labels}
	}
	specDiff := diffSpecs(current.Spec, spec)
	for k, v := range specDiff {
		diff["spec."+k] = v
	}
	if len(diff) == 0 {
		return "", nil, nil
	}
	return "update", diff, nil
}

func orSlashS(f string) string {
	if f == "" {
		return "/"
	}
	return f
}

func orEmptyLabels(l model.Labels) model.Labels {
	if l == nil {
		return model.Labels{}
	}
	return l
}

// docToSpec converts a bundle doc's spec map into an ObjectSpec.
func docToSpec(doc bundle.Doc) (model.ObjectSpec, error) {
	raw, err := json.Marshal(doc.Spec)
	if err != nil {
		return model.ObjectSpec{}, err
	}
	var spec model.ObjectSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return model.ObjectSpec{}, fmt.Errorf("invalid spec: %w", err)
	}
	return spec, nil
}

// diffSpecs compares JSON projections field-wise.
func diffSpecs(old, new model.ObjectSpec) map[string]any {
	diff := map[string]any{}
	oldMap := specMap(old)
	newMap := specMap(new)
	keys := map[string]bool{}
	for k := range oldMap {
		keys[k] = true
	}
	for k := range newMap {
		keys[k] = true
	}
	for k := range keys {
		if !reflect.DeepEqual(oldMap[k], newMap[k]) {
			diff[k] = []any{oldMap[k], newMap[k]}
		}
	}
	return diff
}

func specMap(s model.ObjectSpec) map[string]any {
	raw, _ := json.Marshal(s)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return m
}

// diffDocs compares a stored resource doc against a desired bundle doc.
func diffDocs(current json.RawMessage, desired bundle.Doc) map[string]any {
	var cur map[string]any
	_ = json.Unmarshal(current, &cur)
	// strip envelope fields
	for _, k := range []string{"id", "tenantId", "version", "createdAt", "updatedAt", "name"} {
		delete(cur, k)
	}
	des := map[string]any{}
	for k, v := range desired.Spec {
		des[k] = v
	}
	for k, v := range desired.Data {
		des[k] = v
	}
	if len(desired.Metadata.Labels) > 0 {
		des["labels"] = toAnyMap(desired.Metadata.Labels)
	}
	diff := map[string]any{}
	keys := map[string]bool{}
	for k := range cur {
		keys[k] = true
	}
	for k := range des {
		keys[k] = true
	}
	for k := range keys {
		cv, dv := normJSON(cur[k]), normJSON(des[k])
		if dv == nil {
			continue // absent in bundle = unmanaged field
		}
		if !reflect.DeepEqual(cv, dv) {
			diff[k] = []any{cur[k], des[k]}
		}
	}
	return diff
}

func toAnyMap(m map[string]string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// normJSON canonicalises values via a JSON round-trip for comparison.
func normJSON(v any) any {
	if v == nil {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	_ = json.Unmarshal(raw, &out)
	return out
}

// applyBundle executes a plan transactionally-ish (objects first by
// kind order; failures abort and report what was applied).
func (a *API) applyBundle(w http.ResponseWriter, r *http.Request, p *auth.Principal,
	docs []bundle.Doc, prune bool, pruneSel string) {
	tenant := a.tenantOf(r, p)
	plan, warnings, err := a.computePlan(r.Context(), tenant, docs, prune, pruneSel)
	if err != nil {
		a.validationError(w, r, "bundle", err.Error())
		return
	}
	applied := []PlanAction{}
	// kind-ordered docs (dependencies first)
	ordered := make([]bundle.Doc, len(docs))
	copy(ordered, docs)
	sort.SliceStable(ordered, func(i, j int) bool {
		return kindRankOf(ordered[i].Kind) < kindRankOf(ordered[j].Kind)
	})
	for _, doc := range ordered {
		act, err := a.applyDoc(r.Context(), tenant, doc)
		if err != nil {
			a.audit(r, p, "bundle.apply", "", nil, map[string]any{
				"applied": applied, "failedAt": doc.Ident(), "error": err.Error()})
			a.problem(w, r, http.StatusUnprocessableEntity, "np:bundle/apply",
				"apply failed at "+doc.Ident(), err.Error())
			return
		}
		if act != "" {
			applied = append(applied, PlanAction{Action: act, Kind: doc.Kind,
				Name: doc.Metadata.Name, Host: doc.Metadata.Host})
		}
	}
	// prune deletions
	for _, action := range plan {
		if action.Action != "delete" {
			continue
		}
		if err := a.deleteByPlan(r.Context(), tenant, action); err != nil {
			warnings = append(warnings, "prune "+action.Kind+"/"+action.Name+": "+err.Error())
		} else {
			applied = append(applied, action)
		}
	}
	a.audit(r, p, "bundle.apply", "", nil, map[string]any{"applied": applied})
	a.configChanged(r.Context(), tenant, "object", storage.KindAlertRule)
	a.writeJSON(w, http.StatusOK, PlanResult{Plan: applied, Warnings: warnings})
}

func kindRankOf(kind string) int {
	for i, k := range bundle.KindOrder {
		if k == kind {
			return i
		}
	}
	return len(bundle.KindOrder)
}

// applyDoc upserts one document; returns "create"/"update"/"".
func (a *API) applyDoc(ctx context.Context, tenantID string, doc bundle.Doc) (string, error) {
	switch doc.Kind {
	case "Host", "Service":
		spec, err := docToSpec(doc)
		if err != nil {
			return "", err
		}
		kind := model.KindHost
		hostID := ""
		if doc.Kind == "Service" {
			kind = model.KindService
			host, err := a.Store.GetObjectByName(ctx, tenantID, model.KindHost, "", doc.Metadata.Host)
			if err != nil {
				return "", fmt.Errorf("unknown host %q", doc.Metadata.Host)
			}
			hostID = host.ID
		}
		current, err := a.Store.GetObjectByName(ctx, tenantID, kind, hostID, doc.Metadata.Name)
		if err == storage.ErrNotFound {
			obj := &model.Object{TenantID: tenantID, Kind: kind, Name: doc.Metadata.Name,
				HostID: hostID, Folder: doc.Metadata.Folder,
				Labels: doc.Metadata.Labels, Spec: spec}
			if err := a.validateSpec(ctx, obj); err != nil {
				return "", err
			}
			return "create", a.Store.CreateObject(ctx, obj)
		}
		if err != nil {
			return "", err
		}
		if orSlashS(doc.Metadata.Folder) == current.Folder &&
			reflect.DeepEqual(orEmptyLabels(model.Labels(doc.Metadata.Labels)), orEmptyLabels(current.Labels)) &&
			len(diffSpecs(current.Spec, spec)) == 0 {
			return "", nil // no change
		}
		current.Folder = orSlashS(doc.Metadata.Folder)
		current.Labels = doc.Metadata.Labels
		current.Spec = spec
		if err := a.validateSpec(ctx, current); err != nil {
			return "", err
		}
		return "update", a.Store.UpdateObject(ctx, current, 0)
	default:
		kind, ok := bundleKindToStorage[doc.Kind]
		if !ok {
			return "", nil // warned during plan
		}
		body := map[string]any{}
		for k, v := range doc.Spec {
			body[k] = v
		}
		for k, v := range doc.Data {
			body[k] = v
		}
		if len(doc.Metadata.Labels) > 0 {
			body["labels"] = doc.Metadata.Labels
		}
		body["name"] = doc.Metadata.Name
		if err := a.validateResourceDoc(kind, body); err != nil {
			return "", err
		}
		current, err := a.Store.GetResource(ctx, tenantID, kind, doc.Metadata.Name)
		if err == storage.ErrNotFound {
			_, err := a.Store.PutResource(ctx, tenantID, kind, doc.Metadata.Name, body, -1)
			return "create", err
		}
		if err != nil {
			return "", err
		}
		if len(diffDocs(current.Doc, doc)) == 0 {
			return "", nil
		}
		_, err = a.Store.PutResource(ctx, tenantID, kind, doc.Metadata.Name, body, 0)
		return "update", err
	}
}

func (a *API) deleteByPlan(ctx context.Context, tenantID string, action PlanAction) error {
	switch action.Kind {
	case "Host", "Service":
		kind := model.KindHost
		hostID := ""
		if action.Kind == "Service" {
			kind = model.KindService
			host, err := a.Store.GetObjectByName(ctx, tenantID, model.KindHost, "", action.Host)
			if err != nil {
				return err
			}
			hostID = host.ID
		}
		obj, err := a.Store.GetObjectByName(ctx, tenantID, kind, hostID, action.Name)
		if err != nil {
			return err
		}
		a.Sched.Remove(obj.ID)
		a.Pipe.Forget(obj.ID)
		return a.Store.DeleteObject(ctx, tenantID, obj.ID)
	default:
		kind, ok := bundleKindToStorage[action.Kind]
		if !ok {
			return fmt.Errorf("unsupported kind")
		}
		return a.Store.DeleteResource(ctx, tenantID, kind, action.Name)
	}
}

// exportBundle renders the canonical bundle of a tenant (SPEC §11.6).
func (a *API) exportBundle(ctx context.Context, tenantID, folder string) ([]bundle.Doc, error) {
	var docs []bundle.Doc
	// objects
	objs, err := a.Store.ListObjects(ctx, storage.ObjectFilter{
		TenantID: tenantID, Folder: folder, Limit: 5000})
	if err != nil {
		return nil, err
	}
	hostNames := map[string]string{}
	for _, o := range objs {
		if o.Kind == model.KindHost {
			hostNames[o.ID] = o.Name
		}
	}
	for _, o := range objs {
		doc := bundle.Doc{
			Kind:     titleKind(string(o.Kind)),
			Metadata: bundle.Metadata{Name: o.Name, Folder: o.Folder, Labels: o.Labels},
		}
		if o.Kind == model.KindService {
			doc.Metadata.Host = hostNames[o.HostID]
			if doc.Metadata.Host == "" {
				if h, err := a.Store.GetObject(ctx, tenantID, o.HostID); err == nil {
					doc.Metadata.Host = h.Name
				}
			}
		}
		raw, _ := json.Marshal(o.Spec)
		var spec map[string]any
		_ = json.Unmarshal(raw, &spec)
		doc.Spec = spec
		docs = append(docs, doc)
	}
	if folder != "" {
		return docs, nil // folder exports skip global resources
	}
	// resource kinds
	for bundleKind, storageKind := range bundleKindToStorage {
		if storageKind == storage.KindRole {
			continue // roles export via admin tooling only
		}
		envs, err := a.Store.ListResources(ctx, tenantID, storageKind, "", "", 2000)
		if err != nil {
			return nil, err
		}
		for _, env := range envs {
			var body map[string]any
			if err := json.Unmarshal(env.Doc, &body); err != nil {
				continue
			}
			for _, k := range []string{"id", "tenantId", "version", "createdAt", "updatedAt", "name"} {
				delete(body, k)
			}
			doc := bundle.Doc{Kind: bundleKind,
				Metadata: bundle.Metadata{Name: env.Name}, Spec: body}
			if labels, ok := body["labels"].(map[string]any); ok {
				delete(body, "labels")
				doc.Metadata.Labels = map[string]string{}
				for k, v := range labels {
					doc.Metadata.Labels[k] = fmt.Sprint(v)
				}
			}
			docs = append(docs, doc)
		}
	}
	return docs, nil
}

// titleKind capitalizes the first ASCII letter of an object kind
// ("host" -> "Host", "service" -> "Service") to match bundle Doc kinds.
// Object kinds are always single lowercase ASCII words, so this is a safe
// replacement for the deprecated strings.Title.
func titleKind(s string) string {
	if s == "" {
		return s
	}
	b := []byte(s)
	if b[0] >= 'a' && b[0] <= 'z' {
		b[0] -= 'a' - 'A'
	}
	return string(b)
}

var _ = kindResourceMap
