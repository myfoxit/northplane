package api

// ResourceAdmin methods (the MCP generic config tools' backing): same
// validation as REST, optimistic concurrency, cache invalidation safety.

import (
	"strings"
	"testing"
)

func TestResourceAdminRoundTrip(t *testing.T) {
	ta := bootAPI(t)
	a, ctx := ta.a, ta.ctx

	doc := map[string]any{"name": "ra-mail", "type": "email", "enabled": true,
		"config": map[string]any{"provider": "sendmail"}}
	if _, err := a.UpsertResourceDoc(ctx, "default", "channel", "ra-mail", doc, 0); err != nil {
		t.Fatal(err)
	}

	got, err := a.GetResourceDoc(ctx, "default", "channel", "ra-mail")
	if err != nil || !strings.Contains(string(got), `"sendmail"`) {
		t.Fatalf("get: %v %s", err, got)
	}

	docs, err := a.ListResourceDocs(ctx, "default", "channel", "", 50)
	if err != nil || len(docs) != 1 {
		t.Fatalf("list: %v n=%d", err, len(docs))
	}

	// stale version is refused (optimistic concurrency like REST If-Match)
	if _, err := a.UpsertResourceDoc(ctx, "default", "channel", "ra-mail", doc, 99); err == nil {
		t.Fatal("want version conflict")
	}

	if err := a.DeleteResourceDoc(ctx, "default", "channel", "ra-mail"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.GetResourceDoc(ctx, "default", "channel", "ra-mail"); err == nil {
		t.Fatal("want not-found after delete")
	}
}

func TestResourceAdminValidates(t *testing.T) {
	ta := bootAPI(t)
	a, ctx := ta.a, ta.ctx

	// kind-specific validation: an alert rule with a broken expression is
	// rejected exactly like the REST handler would reject it.
	_, err := a.UpsertResourceDoc(ctx, "default", "alert-rule", "broken", map[string]any{
		"name": "broken", "match": map[string]any{}, "expr": "this is ((( not CEL"}, 0)
	if err == nil {
		t.Fatal("want validation error for broken alert rule")
	}

	// channel without a type is refused
	if _, err := a.UpsertResourceDoc(ctx, "default", "channel", "untyped",
		map[string]any{"name": "untyped"}, 0); err == nil ||
		!strings.Contains(err.Error(), "type required") {
		t.Fatalf("channel validation: %v", err)
	}

	// doc.name mismatching the resource name is refused
	if _, err := a.UpsertResourceDoc(ctx, "default", "channel", "a",
		map[string]any{"name": "b", "type": "email"}, 0); err == nil ||
		!strings.Contains(err.Error(), "does not match") {
		t.Fatalf("name mismatch: %v", err)
	}
}
