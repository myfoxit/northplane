package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/northplane/northplane/internal/auth"
	"github.com/northplane/northplane/internal/storage"
)

// Provider connections (SPEC §10 evolution): a user connects one or
// more LLM accounts; keys are sealed with the platform SecretBox and
// never leave the server. Admins may create tenant-wide ("shared")
// connections every user of the tenant can chat with.

// ConnectionInput is the create/update payload.
type ConnectionInput struct {
	Name         string            `json:"name"`
	Provider     string            `json:"provider"`
	Endpoint     string            `json:"endpoint,omitempty"`
	APIKey       *string           `json:"apiKey,omitempty"` // nil = keep stored key
	DefaultModel string            `json:"defaultModel,omitempty"`
	Extra        map[string]string `json:"extra,omitempty"`
	Shared       bool              `json:"shared,omitempty"` // tenant-wide (admin:ai)
	Disabled     bool              `json:"disabled,omitempty"`
}

func (s *Service) requireBox() (*auth.SecretBox, error) {
	if s.box == nil {
		return nil, fmt.Errorf("secret store disabled — configure secretKeyFile to store provider keys")
	}
	return s.box, nil
}

// validateConnectionInput normalises and authorises the payload.
// Custom endpoints imply server-side requests to arbitrary URLs, so we
// gate them behind config:write (the same trust level that may already
// point webhooks and checks anywhere).
func (s *Service) validateConnectionInput(p *auth.Principal, in *ConnectionInput) (*ProviderType, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return nil, fmt.Errorf("name required")
	}
	pt := ProviderTypeByID(in.Provider)
	if pt == nil {
		return nil, fmt.Errorf("unknown provider %q", in.Provider)
	}
	in.Endpoint = strings.TrimRight(strings.TrimSpace(in.Endpoint), "/")
	if in.Endpoint != "" && in.Endpoint != pt.Endpoint {
		if !strings.HasPrefix(in.Endpoint, "http://") && !strings.HasPrefix(in.Endpoint, "https://") {
			return nil, fmt.Errorf("endpoint must be http(s)")
		}
		if !p.Allow("config:write") {
			return nil, fmt.Errorf("custom endpoints require config:write")
		}
	}
	if in.Endpoint == "" && pt.Endpoint == "" {
		return nil, fmt.Errorf("provider %q needs an explicit endpoint", pt.ID)
	}
	return pt, nil
}

// keyHint keeps the last 4 characters for display ("…a1b2").
func keyHint(key string) string {
	if len(key) <= 4 {
		return "…"
	}
	return "…" + key[len(key)-4:]
}

// ListConnections returns what the caller can use (own + shared).
func (s *Service) ListConnections(ctx context.Context, p *auth.Principal) ([]*storage.AIProviderConnection, error) {
	return s.store.ListAIConnections(ctx, p.TenantID, p.ActorID)
}

// CreateConnection stores a connection for the caller (or tenant-wide
// when Shared — the API layer enforces admin:ai for that).
func (s *Service) CreateConnection(ctx context.Context, p *auth.Principal, in ConnectionInput) (*storage.AIProviderConnection, error) {
	pt, err := s.validateConnectionInput(p, &in)
	if err != nil {
		return nil, err
	}
	conn := &storage.AIProviderConnection{
		TenantID: p.TenantID, UserID: p.ActorID,
		Name: in.Name, Provider: pt.ID, Endpoint: in.Endpoint,
		DefaultModel: in.DefaultModel, Extra: in.Extra, Disabled: in.Disabled,
	}
	if in.Shared {
		conn.UserID = ""
	}
	if in.APIKey != nil && *in.APIKey != "" {
		box, err := s.requireBox()
		if err != nil {
			return nil, err
		}
		sealed, err := box.Seal(strings.TrimSpace(*in.APIKey))
		if err != nil {
			return nil, err
		}
		conn.APIKeySealed = sealed
		conn.KeyHint = keyHint(strings.TrimSpace(*in.APIKey))
	} else if pt.NeedsKey {
		return nil, fmt.Errorf("provider %q requires an API key", pt.ID)
	}
	if err := s.store.CreateAIConnection(ctx, conn); err != nil {
		return nil, err
	}
	s.audit(ctx, p, "ai.connection.create", conn.ID, mustJSON(map[string]any{
		"name": conn.Name, "provider": conn.Provider, "shared": conn.Shared}))
	conn.HasKey = len(conn.APIKeySealed) > 0
	conn.Shared = conn.UserID == ""
	return conn, nil
}

// UpdateConnection rewrites a connection the caller owns (shared ones
// require admin:ai, enforced by the API layer passing shared=true).
func (s *Service) UpdateConnection(ctx context.Context, p *auth.Principal, id string, shared bool, in ConnectionInput) (*storage.AIProviderConnection, error) {
	owner := p.ActorID
	if shared {
		owner = ""
	}
	existing, err := s.store.GetAIConnection(ctx, p.TenantID, owner, id)
	if err != nil {
		return nil, err
	}
	if existing.UserID != owner {
		return nil, storage.ErrNotFound
	}
	in.Provider = existing.Provider // provider type is immutable
	if _, err := s.validateConnectionInput(p, &in); err != nil {
		return nil, err
	}
	existing.Name, existing.Endpoint = in.Name, in.Endpoint
	existing.DefaultModel, existing.Extra, existing.Disabled = in.DefaultModel, in.Extra, in.Disabled
	var newKey []byte
	if in.APIKey != nil {
		key := strings.TrimSpace(*in.APIKey)
		if key == "" {
			newKey = []byte{} // clear
			existing.KeyHint = ""
		} else {
			box, err := s.requireBox()
			if err != nil {
				return nil, err
			}
			sealed, err := box.Seal(key)
			if err != nil {
				return nil, err
			}
			newKey = sealed
			existing.KeyHint = keyHint(key)
		}
	}
	if err := s.store.UpdateAIConnection(ctx, existing, newKey); err != nil {
		return nil, err
	}
	s.audit(ctx, p, "ai.connection.update", id, mustJSON(map[string]any{
		"name": existing.Name, "keyRotated": in.APIKey != nil}))
	return s.store.GetAIConnection(ctx, p.TenantID, owner, id)
}

// DeleteConnection removes a connection.
func (s *Service) DeleteConnection(ctx context.Context, p *auth.Principal, id string, shared bool) error {
	owner := p.ActorID
	if shared {
		owner = ""
	}
	if err := s.store.DeleteAIConnection(ctx, p.TenantID, owner, id); err != nil {
		return err
	}
	s.audit(ctx, p, "ai.connection.delete", id, nil)
	return nil
}

// adapterFor opens the sealed key and builds the wire adapter.
func (s *Service) adapterFor(conn *storage.AIProviderConnection) (StreamProvider, *ProviderType, error) {
	pt := ProviderTypeByID(conn.Provider)
	if pt == nil {
		return nil, nil, fmt.Errorf("unknown provider %q", conn.Provider)
	}
	if conn.Disabled {
		return nil, nil, fmt.Errorf("connection %q is disabled", conn.Name)
	}
	endpoint := conn.Endpoint
	if endpoint == "" {
		endpoint = pt.Endpoint
	}
	if endpoint == "" {
		return nil, nil, fmt.Errorf("connection %q has no endpoint", conn.Name)
	}
	var apiKey string
	if len(conn.APIKeySealed) > 0 {
		box, err := s.requireBox()
		if err != nil {
			return nil, nil, err
		}
		apiKey, err = box.Open(conn.APIKeySealed)
		if err != nil {
			return nil, nil, err
		}
	} else if pt.NeedsKey {
		return nil, nil, fmt.Errorf("connection %q has no API key", conn.Name)
	}
	switch pt.Kind {
	case KindAnthropic:
		return &anthropicStream{endpoint: endpoint, apiKey: apiKey}, pt, nil
	default:
		return &openAIStreamAdapter{endpoint: endpoint, apiKey: apiKey,
			quirks: pt.quirks, baseURL: s.baseURL}, pt, nil
	}
}

// ConnectionModels merges the provider's live listing with the curated
// catalog; when the listing fails the curated set still works.
func (s *Service) ConnectionModels(ctx context.Context, p *auth.Principal, id string) ([]ModelInfo, string, error) {
	conn, err := s.store.GetAIConnection(ctx, p.TenantID, p.ActorID, id)
	if err != nil {
		return nil, "", err
	}
	adapter, pt, err := s.adapterFor(conn)
	if err != nil {
		return nil, "", err
	}
	curated := map[string]ModelInfo{}
	var out []ModelInfo
	for _, m := range pt.Models {
		curated[m.ID] = m
		out = append(out, m)
	}
	live, err := adapter.ListModels(ctx)
	if err != nil {
		note := fmt.Sprintf("live model listing failed: %v", err)
		if len(out) == 0 {
			return nil, "", fmt.Errorf("no models available: %s", note)
		}
		return out, note, nil
	}
	for _, m := range live {
		if _, ok := curated[m.ID]; ok {
			continue
		}
		out = append(out, m)
	}
	// Keep curated first (they are ordered by preference), live rest sorted.
	rest := out[len(curated):]
	sort.Slice(rest, func(i, j int) bool { return rest[i].ID < rest[j].ID })
	return out, "", nil
}

// TestConnection verifies credentials/endpoint by listing models.
func (s *Service) TestConnection(ctx context.Context, p *auth.Principal, id string) (int, error) {
	conn, err := s.store.GetAIConnection(ctx, p.TenantID, p.ActorID, id)
	if err != nil {
		return 0, err
	}
	adapter, pt, err := s.adapterFor(conn)
	if err != nil {
		return 0, err
	}
	models, err := adapter.ListModels(ctx)
	if err != nil {
		return 0, err
	}
	n := len(models)
	if n == 0 {
		n = len(pt.Models)
	}
	return n, nil
}

func mustJSON(v any) json.RawMessage {
	raw, _ := json.Marshal(v)
	return raw
}
