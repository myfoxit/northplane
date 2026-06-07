// Package catalog holds the in-memory view of monitorable configuration
// — objects with resolved effective specs, templates, check commands and
// time periods — so the scheduler and result pipeline never touch SQL on
// the hot path (SPEC §7.4). Mutating API handlers call Invalidate*.
package catalog

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/selector"
	"github.com/northplane/northplane/internal/storage"
)

// Entry is a fully resolved object.
type Entry struct {
	Object    *model.Object
	Effective model.ObjectSpec
	Chain     []string // template chain
	// Exec resolution:
	Class     model.CommandType
	Builtin   string   // builtin check name
	Argv      []string // what to run (exec: argv incl. path; builtin: flags) — unexpanded macros
	MacroArgs []string // $ARG1$… values (the object's args, SPEC §8.2)
	EnvOn     bool
	Host      *Entry // services: their host
	TimePeriod *model.TimePeriod
}

// Catalog is the cache.
type Catalog struct {
	store *storage.Store

	mu       sync.RWMutex
	entries  map[string]*Entry            // object id →
	byName   map[string]string            // tenant/kind/hostID/name → id
	children map[string][]string          // host id → service ids
	parents  map[string][]string          // host id → parent host ids
	templates map[string]*model.Template  // tenant/name →
	commands map[string]*model.CheckCommand
	periods  map[string]*model.TimePeriod
}

// New creates an empty catalog.
func New(store *storage.Store) *Catalog {
	return &Catalog{
		store:    store,
		entries:  map[string]*Entry{},
		byName:   map[string]string{},
		children: map[string][]string{},
		parents:  map[string][]string{},
		templates: map[string]*model.Template{},
		commands: map[string]*model.CheckCommand{},
		periods:  map[string]*model.TimePeriod{},
	}
}

// LoadAll (re)builds the cache from storage for all tenants.
func (c *Catalog) LoadAll(ctx context.Context) error {
	tenants, err := c.store.Tenants(ctx)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = map[string]*Entry{}
	c.byName = map[string]string{}
	c.children = map[string][]string{}
	c.parents = map[string][]string{}
	c.templates = map[string]*model.Template{}
	c.commands = map[string]*model.CheckCommand{}
	c.periods = map[string]*model.TimePeriod{}

	for _, t := range tenants {
		if err := c.loadTenantLocked(ctx, t.ID); err != nil {
			return err
		}
	}
	return nil
}

// ReloadTenant refreshes one tenant (config mutation hook).
func (c *Catalog) ReloadTenant(ctx context.Context, tenantID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	// drop tenant's entries and their graph edges. children/parents must
	// be cleared too, else loadTenantLocked re-appends and duplicates every
	// edge on each config edit (inflating downtime depth / UNREACHABLE).
	for id, e := range c.entries {
		if e.Object.TenantID == tenantID {
			delete(c.entries, id)
			delete(c.children, id)
			delete(c.parents, id)
		}
	}
	for k := range c.byName {
		if strings.HasPrefix(k, tenantID+"/") {
			delete(c.byName, k)
		}
	}
	for k := range c.templates {
		if strings.HasPrefix(k, tenantID+"/") {
			delete(c.templates, k)
		}
	}
	for k := range c.commands {
		if strings.HasPrefix(k, tenantID+"/") {
			delete(c.commands, k)
		}
	}
	for k := range c.periods {
		if strings.HasPrefix(k, tenantID+"/") {
			delete(c.periods, k)
		}
	}
	return c.loadTenantLocked(ctx, tenantID)
}

func (c *Catalog) loadTenantLocked(ctx context.Context, tenantID string) error {
	templates, err := storage.LoadAll[model.Template](ctx, c.store, tenantID, storage.KindTemplate)
	if err != nil {
		return err
	}
	for _, t := range templates {
		c.templates[tenantID+"/"+t.Name] = t
	}
	commands, err := storage.LoadAll[model.CheckCommand](ctx, c.store, tenantID, storage.KindCheckCommand)
	if err != nil {
		return err
	}
	for _, cmd := range commands {
		c.commands[tenantID+"/"+cmd.Name] = cmd
	}
	periods, err := storage.LoadAll[model.TimePeriod](ctx, c.store, tenantID, storage.KindTimePeriod)
	if err != nil {
		return err
	}
	for _, p := range periods {
		c.periods[tenantID+"/"+p.Name] = p
	}

	// objects in pages
	cursor := ""
	for {
		objs, err := c.store.ListObjects(ctx, storage.ObjectFilter{
			TenantID: tenantID, Cursor: cursor, Limit: 2000})
		if err != nil {
			return err
		}
		if len(objs) == 0 {
			break
		}
		for _, o := range objs {
			if err := c.indexLocked(o); err != nil {
				// config errors must not kill the loop — entry is
				// indexed with zeroed effective spec and surfaces in
				// the UI as config error
				e := &Entry{Object: o, Effective: model.SpecDefaults}
				c.entries[o.ID] = e
				c.byName[nameKey(o.TenantID, o.Kind, o.HostID, o.Name)] = o.ID
			}
		}
		cursor = objs[len(objs)-1].ID
		if len(objs) < 2000 {
			break
		}
	}

	// link services ↔ hosts and parent graph
	for _, e := range c.entries {
		if e.Object.TenantID != tenantID {
			continue
		}
		if e.Object.Kind == model.KindService && e.Object.HostID != "" {
			e.Host = c.entries[e.Object.HostID]
			c.children[e.Object.HostID] = append(c.children[e.Object.HostID], e.Object.ID)
		}
		if e.Object.Kind == model.KindHost {
			for _, pname := range e.Effective.Parents {
				if pid, ok := c.byName[nameKey(tenantID, model.KindHost, "", pname)]; ok {
					c.parents[e.Object.ID] = append(c.parents[e.Object.ID], pid)
				}
			}
		}
	}
	return nil
}

func nameKey(tenant string, kind model.Kind, hostID, name string) string {
	return tenant + "/" + string(kind) + "/" + hostID + "/" + name
}

func (c *Catalog) indexLocked(o *model.Object) error {
	eff, chain, err := model.EffectiveSpec(o, func(name string) *model.Template {
		return c.templates[o.TenantID+"/"+name]
	})
	if err != nil {
		return err
	}
	e := &Entry{Object: o, Effective: eff, Chain: chain}
	if err := c.resolveCommandLocked(e); err != nil {
		return err
	}
	if p, ok := c.periods[o.TenantID+"/"+eff.CheckPeriod]; ok {
		e.TimePeriod = p
	}
	c.entries[o.ID] = e
	c.byName[nameKey(o.TenantID, o.Kind, o.HostID, o.Name)] = o.ID
	return nil
}

func (c *Catalog) resolveCommandLocked(e *Entry) error {
	ref := e.Effective.CheckCommand
	class, rest, err := model.ParseCommandRef(ref)
	if err != nil {
		return err
	}
	e.MacroArgs = e.Effective.Args
	switch class {
	case model.CommandPassive:
		e.Class = model.CommandPassive
	case model.CommandBuiltin:
		// "builtin:tcp" + spec args as flags
		e.Class = model.CommandBuiltin
		e.Builtin = rest
		e.Argv = e.Effective.Args
	case model.CommandExec:
		// "exec:check_postgres" + spec args inline
		e.Class = model.CommandExec
		e.Argv = append([]string{rest}, e.Effective.Args...)
	case model.CommandAgent:
		e.Class = model.CommandAgent
		e.Argv = append([]string{strings.TrimPrefix(rest, "exec:")}, e.Effective.Args...)
	default:
		// Named CheckCommand: its line carries $ARGn$ placeholders, the
		// object's args are the macro values (SPEC §8.2).
		cmd, ok := c.commands[e.Object.TenantID+"/"+rest]
		if !ok {
			return fmt.Errorf("unknown check command %q", rest)
		}
		e.EnvOn = cmd.Env
		switch cmd.Type {
		case model.CommandBuiltin:
			e.Class = model.CommandBuiltin
			if len(cmd.Line) > 0 {
				e.Builtin = cmd.Line[0]
			}
			e.Argv = append([]string{}, cmd.Line[1:]...)
		case model.CommandPassive:
			e.Class = model.CommandPassive
		default:
			e.Class = cmd.Type
			e.Argv = append([]string{}, cmd.Line...)
		}
	}
	return nil
}

// Get returns the entry for an object id.
func (c *Catalog) Get(id string) *Entry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.entries[id]
}

// GetByName resolves tenant/kind/host/name.
func (c *Catalog) GetByName(tenantID string, kind model.Kind, hostID, name string) *Entry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if id, ok := c.byName[nameKey(tenantID, kind, hostID, name)]; ok {
		return c.entries[id]
	}
	return nil
}

// All snapshots every entry (scheduler bootstrap).
func (c *Catalog) All() []*Entry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]*Entry, 0, len(c.entries))
	for _, e := range c.entries {
		out = append(out, e)
	}
	return out
}

// Children returns service ids of a host.
func (c *Catalog) Children(hostID string) []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]string(nil), c.children[hostID]...)
}

// Parents returns parent host ids (reachability graph, SPEC §6.3).
func (c *Catalog) Parents(hostID string) []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]string(nil), c.parents[hostID]...)
}

// Select returns entries matching a label selector (in-memory; used by
// downtimes, silences, dashboards).
func (c *Catalog) Select(tenantID string, sel selector.Selector) []*Entry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out []*Entry
	for _, e := range c.entries {
		if e.Object.TenantID != tenantID {
			continue
		}
		if sel.Matches(e.Object.Labels) {
			out = append(out, e)
		}
	}
	return out
}

// Period resolves a named time period of a tenant.
func (c *Catalog) Period(tenantID, name string) *model.TimePeriod {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.periods[tenantID+"/"+name]
}

// Size returns the number of cached objects.
func (c *Catalog) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}
