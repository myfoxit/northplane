package storage

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/northplane/northplane/internal/model"
)

// Both suites run on the full storage matrix (SQLite always, PostgreSQL
// when NORTHPLANE_TEST_PG_DSN is set) — the agent-chat tables must be
// first-class on both dialects like every other table.

func TestAIConnectionCRUDAndScoping(t *testing.T) {
	matrix(t, testAIConnectionCRUDAndScoping)
}

func testAIConnectionCRUDAndScoping(t *testing.T, s *Store) {
	ctx := context.Background()
	tenant := model.DefaultTenant

	own := &AIProviderConnection{TenantID: tenant, UserID: "alice", Name: "mine",
		Provider: "anthropic", APIKeySealed: []byte{1, 2, 3}, KeyHint: "…abcd"}
	if err := s.CreateAIConnection(ctx, own); err != nil {
		t.Fatal(err)
	}
	shared := &AIProviderConnection{TenantID: tenant, UserID: "", Name: "team",
		Provider: "openai"}
	if err := s.CreateAIConnection(ctx, shared); err != nil {
		t.Fatal(err)
	}

	// duplicate name for same owner → ErrDuplicate
	dup := &AIProviderConnection{TenantID: tenant, UserID: "alice", Name: "mine", Provider: "openai"}
	if err := s.CreateAIConnection(ctx, dup); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("want ErrDuplicate, got %v", err)
	}

	// alice sees own + shared; bob sees only shared
	alice, err := s.ListAIConnections(ctx, tenant, "alice")
	if err != nil || len(alice) != 2 {
		t.Fatalf("alice list: %d %v", len(alice), err)
	}
	bob, err := s.ListAIConnections(ctx, tenant, "bob")
	if err != nil || len(bob) != 1 || !bob[0].Shared {
		t.Fatalf("bob list: %+v %v", bob, err)
	}

	// bob cannot fetch alice's connection
	if _, err := s.GetAIConnection(ctx, tenant, "bob", own.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user get must 404, got %v", err)
	}
	// but everyone may fetch shared
	if _, err := s.GetAIConnection(ctx, tenant, "bob", shared.ID); err != nil {
		t.Fatalf("shared get: %v", err)
	}

	// update keeps key when newKey nil, clears on empty slice
	own.Name = "renamed"
	if err := s.UpdateAIConnection(ctx, own, nil); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetAIConnection(ctx, tenant, "alice", own.ID)
	if got.Name != "renamed" || !got.HasKey {
		t.Fatalf("update lost key: %+v", got)
	}
	if err := s.UpdateAIConnection(ctx, own, []byte{}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetAIConnection(ctx, tenant, "alice", own.ID)
	if got.HasKey {
		t.Fatal("key not cleared")
	}

	// delete respects owner
	if err := s.DeleteAIConnection(ctx, tenant, "bob", own.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user delete must 404, got %v", err)
	}
	if err := s.DeleteAIConnection(ctx, tenant, "alice", own.ID); err != nil {
		t.Fatal(err)
	}
}

func TestAIChatMessagesLifecycle(t *testing.T) {
	matrix(t, testAIChatMessagesLifecycle)
}

func testAIChatMessagesLifecycle(t *testing.T, s *Store) {
	ctx := context.Background()
	tenant := model.DefaultTenant

	chat := &AIChat{TenantID: tenant, UserID: "alice", Title: "test"}
	if err := s.CreateAIChat(ctx, chat); err != nil {
		t.Fatal(err)
	}
	// per-user isolation
	if _, err := s.GetAIChat(ctx, tenant, "bob", chat.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user chat get must 404, got %v", err)
	}

	var ids []string
	for i := 0; i < 4; i++ {
		m := &AIChatMessage{ChatID: chat.ID, TenantID: tenant,
			Role: "user", Parts: json.RawMessage(`[{"type":"text","text":"x"}]`)}
		if err := s.AppendAIChatMessage(ctx, m); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, m.ID)
	}
	msgs, err := s.ListAIChatMessages(ctx, tenant, chat.ID)
	if err != nil || len(msgs) != 4 {
		t.Fatalf("list: %d %v", len(msgs), err)
	}
	// insertion order via UUIDv7
	for i := range msgs {
		if msgs[i].ID != ids[i] {
			t.Fatalf("order broken at %d", i)
		}
	}

	// single delete
	if err := s.DeleteAIChatMessage(ctx, tenant, chat.ID, ids[1]); err != nil {
		t.Fatal(err)
	}
	msgs, _ = s.ListAIChatMessages(ctx, tenant, chat.ID)
	if len(msgs) != 3 {
		t.Fatalf("after delete: %d", len(msgs))
	}

	// delete-from (regenerate primitive): removes ids[2] and ids[3]
	if err := s.DeleteAIChatMessagesFrom(ctx, tenant, chat.ID, ids[2]); err != nil {
		t.Fatal(err)
	}
	msgs, _ = s.ListAIChatMessages(ctx, tenant, chat.ID)
	if len(msgs) != 1 || msgs[0].ID != ids[0] {
		t.Fatalf("after delete-from: %+v", msgs)
	}

	// chat delete removes messages
	if err := s.DeleteAIChat(ctx, tenant, "alice", chat.ID); err != nil {
		t.Fatal(err)
	}
	msgs, _ = s.ListAIChatMessages(ctx, tenant, chat.ID)
	if len(msgs) != 0 {
		t.Fatalf("messages survived chat delete: %d", len(msgs))
	}
}
