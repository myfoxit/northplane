package api

// ResourceAdmin implementation (ai.ResourceAdmin): the generic MCP/AI
// config tools reach the resource-document store through these methods so
// they share the REST layer's validation (validateResourceDoc) and cache
// invalidation (configChanged) — one behaviour for every client (P1).

import (
	"context"
	"encoding/json"
	"fmt"
)

// ListResourceDocs lists documents of a kind (name-substring filter).
func (a *API) ListResourceDocs(ctx context.Context, tenantID, kind, query string, limit int) ([]json.RawMessage, error) {
	envs, err := a.Store.ListResources(ctx, tenantID, kind, query, "", limit)
	if err != nil {
		return nil, err
	}
	docs := make([]json.RawMessage, 0, len(envs))
	for _, env := range envs {
		docs = append(docs, env.Doc)
	}
	return docs, nil
}

// GetResourceDoc fetches one document by kind and name (or ID).
func (a *API) GetResourceDoc(ctx context.Context, tenantID, kind, name string) (json.RawMessage, error) {
	env, err := a.Store.ResolveResource(ctx, tenantID, kind, name)
	if err != nil {
		return nil, err
	}
	return env.Doc, nil
}

// UpsertResourceDoc validates and stores a document, then signals the
// config change exactly like the REST PUT/POST handlers. expectVersion
// 0 upserts unconditionally; >0 enforces optimistic concurrency.
func (a *API) UpsertResourceDoc(ctx context.Context, tenantID, kind, name string,
	doc map[string]any, expectVersion int64) (json.RawMessage, error) {
	if name == "" {
		return nil, fmt.Errorf("name required")
	}
	if doc["name"] == nil || doc["name"] == "" {
		doc["name"] = name
	} else if dn, _ := doc["name"].(string); dn != name {
		return nil, fmt.Errorf("doc.name %q does not match resource name %q", dn, name)
	}
	if err := a.validateResourceDoc(kind, doc); err != nil {
		return nil, err
	}
	if a.systemRoleImmutable(ctx, tenantID, kind, name) {
		return nil, fmt.Errorf("system role %q is immutable", name)
	}
	env, err := a.Store.PutResource(ctx, tenantID, kind, name, doc, expectVersion)
	if err != nil {
		return nil, err
	}
	a.configChanged(ctx, tenantID, kind)
	return env.Doc, nil
}

// DeleteResourceDoc removes a document and signals the config change.
func (a *API) DeleteResourceDoc(ctx context.Context, tenantID, kind, name string) error {
	if a.systemRoleImmutable(ctx, tenantID, kind, name) {
		return fmt.Errorf("system role %q is immutable", name)
	}
	if err := a.Store.DeleteResource(ctx, tenantID, kind, name); err != nil {
		return err
	}
	a.configChanged(ctx, tenantID, kind)
	return nil
}
