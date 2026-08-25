package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/northplane/northplane/internal/auth"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/selector"
	"github.com/northplane/northplane/internal/storage"
)

// Business services (SPEC §9.7): tree/DAG, aggregation rules, live
// impact and SLA budgets.

// BSNode is the evaluated tree node.
type BSNode struct {
	Service  *model.BusinessService `json:"service"`
	State    model.State            `json:"state"`
	Children []*BSNode              `json:"children,omitempty"`
	// Causes: worst leaf objects driving the state (impact view).
	Causes []string `json:"causes,omitempty"`
}

func (a *API) registerBusiness() {
	a.resourceCRUD("business-services", storage.KindBusinessService, "config", model.BusinessService{})

	a.handle("GET /api/v1/business-services:tree", "Evaluated BPI tree with live status",
		"objects:read", nil, []*BSNode{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			roots, err := a.evaluateBSTree(r.Context(), a.tenantOf(r, p))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			a.writeJSON(w, http.StatusOK, roots)
		})

	// impact: which business services does an object affect (SPEC §9.7)
	a.handle("GET /api/v1/objects/{id}/impact", "Business services affected by this object",
		"objects:read", nil, []string{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			tenant := a.tenantOf(r, p)
			objID := param(r, "id")
			services, err := storage.LoadAll[model.BusinessService](r.Context(), a.Store,
				tenant, storage.KindBusinessService)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			obj := a.Catalog.Get(objID)
			var affected []string
			for _, bs := range services {
				if bs.ObjectID == objID {
					affected = append(affected, bs.Name)
					continue
				}
				if bs.Selector != "" && obj != nil {
					if sel, err := selector.Parse(bs.Selector); err == nil && sel.Matches(obj.Object.Labels) {
						affected = append(affected, bs.Name)
					}
				}
			}
			// climb to roots: a leaf affects all ancestors
			byID := map[string]*model.BusinessService{}
			byName := map[string]*model.BusinessService{}
			for _, bs := range services {
				byID[bs.ID] = bs
				byName[bs.Name] = bs
			}
			seen := map[string]bool{}
			var out []string
			var climb func(name string)
			climb = func(name string) {
				if seen[name] {
					return
				}
				seen[name] = true
				out = append(out, name)
				node := byName[name]
				if node == nil || node.ParentID == "" {
					return
				}
				if parent := byID[node.ParentID]; parent != nil {
					climb(parent.Name)
				}
			}
			for _, name := range affected {
				climb(name)
			}
			sort.Strings(out)
			a.writeJSON(w, http.StatusOK, out)
		})

	// SLA budget (SPEC §9.7: live einsehbar)
	type slaResponse struct {
		Service      string  `json:"service"`
		Target       float64 `json:"target"`
		WindowDays   int     `json:"windowDays"`
		Availability float64 `json:"availability"`
		BudgetTotal  string  `json:"budgetTotal"` // allowed downtime
		BudgetSpent  string  `json:"budgetSpent"`
		BudgetLeft   string  `json:"budgetLeft"`
	}
	a.handle("GET /api/v1/business-services/{name}/sla", "SLA budget consumption",
		"objects:read", nil, slaResponse{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			tenant := a.tenantOf(r, p)
			bs, err := storage.LoadOne[model.BusinessService](r.Context(), a.Store, tenant,
				storage.KindBusinessService, param(r, "name"))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			days := 30
			switch bs.SLAWindow {
			case "quarter":
				days = 90
			case "year":
				days = 365
			}
			target := bs.SLATarget
			if target <= 0 {
				target = 99.9
			}
			avail, downtime, err := a.availability(r.Context(), tenant, bs, days)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			window := time.Duration(days) * 24 * time.Hour
			budget := time.Duration(float64(window) * (100 - target) / 100)
			left := budget - downtime
			if left < 0 {
				left = 0
			}
			a.writeJSON(w, http.StatusOK, slaResponse{
				Service: bs.Name, Target: target, WindowDays: days,
				Availability: avail,
				BudgetTotal:  budget.Round(time.Minute).String(),
				BudgetSpent:  downtime.Round(time.Minute).String(),
				BudgetLeft:   left.Round(time.Minute).String(),
			})
		})
}

// evaluateBSTree builds and grades the forest.
func (a *API) evaluateBSTree(ctx context.Context, tenantID string) ([]*BSNode, error) {
	services, err := storage.LoadAll[model.BusinessService](ctx, a.Store, tenantID,
		storage.KindBusinessService)
	if err != nil {
		return nil, err
	}
	nodes := map[string]*BSNode{}
	for _, bs := range services {
		nodes[bs.ID] = &BSNode{Service: bs}
	}
	var roots []*BSNode
	for _, bs := range services {
		if bs.ParentID != "" && nodes[bs.ParentID] != nil {
			parent := nodes[bs.ParentID]
			parent.Children = append(parent.Children, nodes[bs.ID])
		} else {
			roots = append(roots, nodes[bs.ID])
		}
	}
	var eval func(n *BSNode) model.State
	eval = func(n *BSNode) model.State {
		bs := n.Service
		// leaf: object or selector
		if len(n.Children) == 0 {
			states := a.leafStates(ctx, tenantID, bs, &n.Causes)
			n.State = aggregate(bs, states)
			return n.State
		}
		var childStates []model.State
		for _, c := range n.Children {
			childStates = append(childStates, eval(c))
			n.Causes = append(n.Causes, c.Causes...)
		}
		if len(n.Causes) > 5 {
			n.Causes = n.Causes[:5]
		}
		n.State = aggregate(bs, childStates)
		return n.State
	}
	for _, root := range roots {
		eval(root)
	}
	sort.Slice(roots, func(i, j int) bool { return roots[i].Service.Name < roots[j].Service.Name })
	return roots, nil
}

func (a *API) leafStates(ctx context.Context, tenantID string, bs *model.BusinessService, causes *[]string) []model.State {
	var states []model.State
	add := func(objID, name string) {
		cs, err := a.Store.GetCheckState(ctx, objID)
		if err != nil {
			return
		}
		states = append(states, cs.State)
		if cs.State != model.StateOK && len(*causes) < 5 {
			*causes = append(*causes, name)
		}
	}
	if bs.ObjectID != "" {
		if e := a.Catalog.Get(bs.ObjectID); e != nil {
			add(bs.ObjectID, e.Object.Name)
		}
	}
	if bs.Selector != "" {
		if sel, err := selector.Parse(bs.Selector); err == nil {
			for _, e := range a.Catalog.Select(tenantID, sel) {
				add(e.Object.ID, e.Object.Name)
			}
		}
	}
	return states
}

// aggregate applies the node rule (SPEC §9.7: worst|best|quorum|weighted).
func aggregate(bs *model.BusinessService, states []model.State) model.State {
	if len(states) == 0 {
		return model.StateUnknown
	}
	switch bs.Rule {
	case model.BSBest:
		best := model.StateCritical
		for _, s := range states {
			if rank(s) < rank(best) {
				best = s
			}
		}
		return best
	case model.BSQuorum:
		pct := bs.QuorumPct
		if pct <= 0 {
			pct = 50
		}
		ok := 0
		for _, s := range states {
			if s == model.StateOK {
				ok++
			}
		}
		if float64(ok)/float64(len(states))*100 >= pct {
			return model.StateOK
		}
		return model.StateCritical
	default: // worst (and weighted approximated as worst in v1)
		worst := model.StateOK
		for _, s := range states {
			if rank(s) > rank(worst) {
				worst = s
			}
		}
		return worst
	}
}

// rank orders states by badness: OK < WARNING < UNKNOWN < CRITICAL.
func rank(s model.State) int {
	switch s {
	case model.StateOK:
		return 0
	case model.StateWarning:
		return 1
	case model.StateUnknown:
		return 2
	default:
		return 3
	}
}

// availability computes uptime over a window from state_change events
// of the service's leaf objects (downtimes excluded when configured).
func (a *API) availability(ctx context.Context, tenantID string, bs *model.BusinessService, days int) (float64, time.Duration, error) {
	var objIDs []string
	if bs.ObjectID != "" {
		objIDs = append(objIDs, bs.ObjectID)
	}
	if bs.Selector != "" {
		if sel, err := selector.Parse(bs.Selector); err == nil {
			for _, e := range a.Catalog.Select(tenantID, sel) {
				objIDs = append(objIDs, e.Object.ID)
			}
		}
	}
	if len(objIDs) == 0 {
		return 100, 0, nil
	}
	from := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour)
	to := time.Now().UTC()
	// union of per-object downtime (worst-case service view: any leaf
	// down = service down — matches the "worst" default rule)
	var totalDown time.Duration
	var sub downtimeSubtractor
	if bs.ExclDowntime {
		sub = a.newDowntimeSubtractor(ctx, tenantID)
	}
	for _, id := range objIDs {
		down, err := ObjectDowntime(ctx, a.Store, tenantID, id, from, to, sub.forObject(id))
		if err != nil {
			return 0, 0, err
		}
		totalDown += down
	}
	if d := time.Duration(days) * 24 * time.Hour; totalDown > d {
		totalDown = d
	}
	window := to.Sub(from)
	avail := 100 * (1 - float64(totalDown)/float64(window))
	return avail, totalDown, nil
}

// downtimeSubtractor reports, per object, how much of an interval lies
// inside a scheduled downtime — the SLA math subtracts that when
// excludeDowntimes is set. The zero value subtracts nothing.
type downtimeSubtractor struct {
	api      *API
	tenantID string
	dts      []*model.Downtime
}

// newDowntimeSubtractor loads the tenant's downtimes once (historic
// windows included — SLA windows reach into the past).
func (a *API) newDowntimeSubtractor(ctx context.Context, tenantID string) downtimeSubtractor {
	dts, err := a.Store.ListDowntimes(ctx, tenantID, false)
	if err != nil {
		a.Log.Warn("sla: downtime load failed; not excluding downtimes", "err", err)
		dts = nil
	}
	return downtimeSubtractor{api: a, tenantID: tenantID, dts: dts}
}

// forObject binds the subtractor to one object ("" matcher when the
// subtractor is the zero value). Overlap is sampled at minute steps —
// downtimes are minute-granular and RRULE occurrences make exact
// interval algebra disproportionate here.
func (s downtimeSubtractor) forObject(objectID string) func(start, end time.Time) time.Duration {
	if s.api == nil || len(s.dts) == 0 {
		return nil
	}
	var mine []*model.Downtime
	for _, d := range s.dts {
		switch {
		case d.ObjectID == objectID:
			mine = append(mine, d)
		case d.ObjectID != "":
			for _, childID := range s.api.Catalog.Children(d.ObjectID) {
				if childID == objectID {
					mine = append(mine, d)
					break
				}
			}
		case d.Selector != "":
			sel, err := selector.Parse(d.Selector)
			if err != nil {
				continue
			}
			for _, e := range s.api.Catalog.Select(s.tenantID, sel) {
				if e.Object.ID == objectID {
					mine = append(mine, d)
					break
				}
			}
		}
	}
	if len(mine) == 0 {
		return nil
	}
	return func(start, end time.Time) time.Duration {
		var in time.Duration
		for t := start; t.Before(end); {
			next := t.Truncate(time.Minute).Add(time.Minute)
			if next.After(end) {
				next = end
			}
			for _, d := range mine {
				if d.ActiveAt(t) {
					in += next.Sub(t)
					break
				}
			}
			t = next
		}
		return in
	}
}

// ObjectDowntime sums non-OK hard time of one object within [from,to)
// from its state_change events plus the current state. inDowntime, when
// non-nil, returns the span of a bad interval covered by scheduled
// downtimes; that span is not counted (excludeDowntimes).
func ObjectDowntime(ctx context.Context, store *storage.Store, tenantID, objectID string,
	from, to time.Time, inDowntime func(start, end time.Time) time.Duration) (time.Duration, error) {
	events, err := store.QueryEvents(ctx, storage.EventFilter{
		TenantID: tenantID, ObjectID: objectID,
		Types: []string{string(model.EventStateChange)},
		From:  from, To: to, Limit: 1000, Asc: true,
	})
	if err != nil {
		return 0, err
	}
	var down time.Duration
	cursor := from
	bad := false // state before window start: assume OK (pessimism would need a snapshot table)
	for _, e := range events {
		var payload model.StateChangePayload
		if err := jsonUnmarshal(e.Payload, &payload); err != nil {
			continue
		}
		if payload.StateType != model.StateHard {
			continue
		}
		nowBad := payload.To != model.StateOK
		if bad && !nowBad {
			span := e.TS.Sub(cursor)
			if inDowntime != nil {
				span -= inDowntime(cursor, e.TS)
			}
			down += span
		}
		if nowBad && !bad {
			cursor = e.TS
		}
		bad = nowBad
	}
	if bad {
		span := to.Sub(cursor)
		if inDowntime != nil {
			span -= inDowntime(cursor, to)
		}
		down += span
	}
	return down, nil
}

func jsonUnmarshal(raw []byte, v any) error {
	return json.Unmarshal(raw, v)
}
