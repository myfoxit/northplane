package api

// HTTP-level coverage for the object (host/service) CRUD surface
// (SPEC §11.3): create→get with ETag, list + selector filter, optimistic
// concurrency via If-Match (200 vs 409 vs 428), delete, 404s, validation
// (422/problem+json), folder scope, and the RBAC matrix (read-only token
// may GET but not mutate; a token without objects:* is forbidden).

import (
	"net/http"
	"testing"
)

func TestObjectsCRUDAndETag(t *testing.T) {
	ta := bootAPI(t)

	// create → 201 + ETag "1" (CreateObject seeds Version=1).
	rec := ta.raw("POST", "/api/v1/hosts", map[string]any{
		"name": "web01", "folder": "/prod",
		"labels": map[string]string{"env": "prod", "role": "web"},
		"spec":   map[string]any{"checkCommand": "passive"},
	}, bearer(ta.adminToken))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("ETag"); got != `"1"` {
		t.Fatalf("create ETag = %q, want \"1\"", got)
	}
	id := ta.id(rec.Body.Bytes())

	// get → 200 + ETag echoes the version + live state present.
	gr := ta.raw("GET", "/api/v1/objects/"+id, nil, bearer(ta.adminToken))
	if gr.Code != 200 {
		t.Fatalf("get: %d %s", gr.Code, gr.Body)
	}
	if got := gr.Header().Get("ETag"); got != `"1"` {
		t.Fatalf("get ETag = %q, want \"1\"", got)
	}

	// update without If-Match → 428 (precondition required).
	code, body := ta.admin("PUT", "/api/v1/objects/"+id, map[string]any{
		"spec": map[string]any{"checkCommand": "passive", "runbook": "step 1"}})
	if code != http.StatusPreconditionRequired {
		t.Fatalf("update no If-Match: want 428, got %d %s", code, body)
	}
	if p := decodeProblem(t, body); p.Code != "np:precondition/if-match" {
		t.Fatalf("bad problem code: %s", body)
	}

	// update with stale If-Match → 409 version conflict.
	code, body = ta.admin("PUT", "/api/v1/objects/"+id,
		map[string]any{"spec": map[string]any{"checkCommand": "passive"}}, ifMatch(99))
	if code != http.StatusConflict {
		t.Fatalf("stale If-Match: want 409, got %d %s", code, body)
	}
	if p := decodeProblem(t, body); p.Code != "np:conflict/version" {
		t.Fatalf("bad conflict code: %s", body)
	}

	// update with correct If-Match → 200, version bumps to 2, ETag "2".
	rec = ta.raw("PUT", "/api/v1/objects/"+id, map[string]any{
		"spec": map[string]any{"checkCommand": "passive", "runbook": "step 1"}},
		bearer(ta.adminToken), ifMatch(1))
	if rec.Code != 200 {
		t.Fatalf("update If-Match=1: %d %s", rec.Code, rec.Body)
	}
	if v := ta.version(rec.Body.Bytes()); v != 2 {
		t.Fatalf("version after update = %d, want 2", v)
	}
	if got := rec.Header().Get("ETag"); got != `"2"` {
		t.Fatalf("update ETag = %q, want \"2\"", got)
	}

	// rename is rejected (422) — names are immutable.
	code, body = ta.admin("PUT", "/api/v1/objects/"+id,
		map[string]any{"name": "renamed", "spec": map[string]any{"checkCommand": "passive"}},
		ifMatch(2))
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("rename: want 422, got %d %s", code, body)
	}

	// delete → 204, then get → 404 problem+json.
	if code, body = ta.admin("DELETE", "/api/v1/objects/"+id, nil); code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", code, body)
	}
	code, body = ta.admin("GET", "/api/v1/objects/"+id, nil)
	if code != http.StatusNotFound {
		t.Fatalf("get after delete: want 404, got %d %s", code, body)
	}
	if p := decodeProblem(t, body); p.Code != "np:not-found" || p.Status != 404 {
		t.Fatalf("bad 404 problem: %s", body)
	}
}

func TestObjectUpdateFolderAndLabels(t *testing.T) {
	ta := bootAPI(t)
	id, _ := ta.createHost("web01", "/prod")

	// update folder + labels (exercises the folder-scope + labels branches).
	rec := ta.raw("PUT", "/api/v1/objects/"+id, map[string]any{
		"folder": "/prod/web",
		"labels": map[string]string{"env": "prod", "tier": "frontend"},
		"spec":   map[string]any{"checkCommand": "passive"},
	}, bearer(ta.adminToken), ifMatch(1))
	if rec.Code != 200 {
		t.Fatalf("update folder+labels: %d %s", rec.Code, rec.Body)
	}
	var obj struct {
		Folder string            `json:"folder"`
		Labels map[string]string `json:"labels"`
	}
	mustJSON(t, rec.Body.Bytes(), &obj)
	if obj.Folder != "/prod/web" || obj.Labels["tier"] != "frontend" {
		t.Fatalf("update did not apply folder/labels: %+v", obj)
	}

	// list with withState=false still returns the object (no state join).
	code, body := ta.admin("GET", "/api/v1/hosts?withState=false", nil)
	if code != 200 {
		t.Fatalf("list withState=false: %d %s", code, body)
	}
}

func TestObjectsGetUnknown404(t *testing.T) {
	ta := bootAPI(t)
	code, body := ta.admin("GET", "/api/v1/objects/np-does-not-exist", nil)
	if code != http.StatusNotFound {
		t.Fatalf("unknown object: want 404, got %d %s", code, body)
	}
	if got := decodeProblem(t, body).Type; got == "" {
		t.Fatalf("problem missing type: %s", body)
	}
}

func TestObjectsListAndSelectorFilter(t *testing.T) {
	ta := bootAPI(t)

	// two prod, one staging.
	for _, h := range []struct {
		name string
		env  string
	}{{"web01", "prod"}, {"web02", "prod"}, {"db01", "staging"}} {
		code, body := ta.admin("POST", "/api/v1/hosts", map[string]any{
			"name": h.name, "labels": map[string]string{"env": h.env},
			"spec": map[string]any{"checkCommand": "passive"}})
		if code != http.StatusCreated {
			t.Fatalf("seed %s: %d %s", h.name, code, body)
		}
	}

	// plain list returns all three.
	code, body := ta.admin("GET", "/api/v1/hosts", nil)
	if code != 200 {
		t.Fatalf("list: %d %s", code, body)
	}
	var list struct {
		Items []struct {
			Name   string            `json:"name"`
			Labels map[string]string `json:"labels"`
		}
	}
	mustJSON(t, body, &list)
	if len(list.Items) != 3 {
		t.Fatalf("list want 3, got %d: %s", len(list.Items), body)
	}

	// selector env=prod returns exactly the two prod hosts.
	code, body = ta.admin("GET", "/api/v1/hosts?selector=env%3Dprod", nil)
	if code != 200 {
		t.Fatalf("selector list: %d %s", code, body)
	}
	mustJSON(t, body, &list)
	if len(list.Items) != 2 {
		t.Fatalf("selector env=prod want 2, got %d: %s", len(list.Items), body)
	}
	for _, it := range list.Items {
		if it.Labels["env"] != "prod" {
			t.Fatalf("selector leaked non-prod host: %+v", it)
		}
	}

	// malformed selector → 422 validation problem.
	code, body = ta.admin("GET", "/api/v1/hosts?selector=%3D%3Dbad", nil)
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("bad selector: want 422, got %d %s", code, body)
	}
	if p := decodeProblem(t, body); p.Code != "np:validation/selector" {
		t.Fatalf("bad selector problem: %s", body)
	}
}

func TestServiceRequiresHost(t *testing.T) {
	ta := bootAPI(t)
	ta.createHost("web01", "/")

	// service referencing a known host → 201.
	code, body := ta.admin("POST", "/api/v1/services", map[string]any{
		"name": "http", "host": "web01",
		"spec": map[string]any{"checkCommand": "passive"}})
	if code != http.StatusCreated {
		t.Fatalf("create service: %d %s", code, body)
	}

	// service referencing an unknown host → 422.
	code, body = ta.admin("POST", "/api/v1/services", map[string]any{
		"name": "http", "host": "ghost",
		"spec": map[string]any{"checkCommand": "passive"}})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown host: want 422, got %d %s", code, body)
	}
	if p := decodeProblem(t, body); p.Code != "np:validation/host" {
		t.Fatalf("bad host problem: %s", body)
	}
}

func TestObjectsValidation(t *testing.T) {
	ta := bootAPI(t)

	cases := []struct {
		name     string
		body     any
		wantCode int
		wantPCod string
	}{
		{"missing name", map[string]any{
			"spec": map[string]any{"checkCommand": "passive"}},
			http.StatusUnprocessableEntity, "np:validation/name"},
		{"malformed json", `{"name": "x", "spec": }`,
			http.StatusUnprocessableEntity, "np:validation/body"},
		{"unknown notifyOn token", map[string]any{
			"name": "x", "spec": map[string]any{
				"checkCommand": "passive", "notifyOn": []string{"bogus"}}},
			http.StatusUnprocessableEntity, "np:validation/spec"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, body := ta.admin("POST", "/api/v1/hosts", c.body)
			if code != c.wantCode {
				t.Fatalf("status = %d, want %d: %s", code, c.wantCode, body)
			}
			if p := decodeProblem(t, body); p.Code != c.wantPCod {
				t.Fatalf("problem code = %q, want %q: %s", p.Code, c.wantPCod, body)
			}
		})
	}
}

func TestObjectSpecContactResolution(t *testing.T) {
	ta := bootAPI(t)

	// referencing an unknown contact in the spec → 422 (validateSpec).
	code, body := ta.admin("POST", "/api/v1/hosts", map[string]any{
		"name": "web01",
		"spec": map[string]any{"checkCommand": "passive", "contacts": []string{"ghost"}}})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown contact in spec: want 422, got %d %s", code, body)
	}
	if p := decodeProblem(t, body); p.Code != "np:validation/spec" {
		t.Fatalf("bad spec problem: %s", body)
	}

	// after seeding the contact, the same spec validates → 201.
	if c, b := ta.admin("POST", "/api/v1/contacts", map[string]any{
		"name": "alice", "email": "alice@example.net"}); c != http.StatusCreated {
		t.Fatalf("seed contact: %d %s", c, b)
	}
	code, body = ta.admin("POST", "/api/v1/hosts", map[string]any{
		"name": "web01",
		"spec": map[string]any{"checkCommand": "passive", "contacts": []string{"alice"}}})
	if code != http.StatusCreated {
		t.Fatalf("known contact in spec: %d %s", code, body)
	}
}

func TestObjectSpecTemplateResolution(t *testing.T) {
	ta := bootAPI(t)
	// a template the object inherits from (validateSpec resolves the chain).
	if c, b := ta.admin("POST", "/api/v1/templates", map[string]any{
		"name": "base", "kind": "host",
		"spec": map[string]any{"checkCommand": "passive", "interval": "30s"}}); c != http.StatusCreated {
		t.Fatalf("seed template: %d %s", c, b)
	}
	// host using the template → effective spec resolves, create succeeds.
	code, body := ta.admin("POST", "/api/v1/hosts", map[string]any{
		"name": "web01",
		"spec": map[string]any{"templates": []string{"base"}}})
	if code != http.StatusCreated {
		t.Fatalf("host with template: %d %s", code, body)
	}
	id := ta.id(body)

	// effective-config reflects the inherited interval.
	code, body = ta.admin("GET", "/api/v1/objects/"+id+"/effective-config", nil)
	if code != 200 {
		t.Fatalf("effective-config: %d %s", code, body)
	}
	var eff struct {
		Chain []string `json:"templateChain"`
	}
	mustJSON(t, body, &eff)
	if len(eff.Chain) == 0 {
		t.Fatalf("template chain not resolved: %s", body)
	}
}

func TestObjectsDuplicate409(t *testing.T) {
	ta := bootAPI(t)
	ta.createHost("web01", "/")
	code, body := ta.admin("POST", "/api/v1/hosts", map[string]any{
		"name": "web01", "spec": map[string]any{"checkCommand": "passive"}})
	if code != http.StatusConflict {
		t.Fatalf("duplicate host: want 409, got %d %s", code, body)
	}
	if p := decodeProblem(t, body); p.Code != "np:conflict/duplicate" {
		t.Fatalf("bad duplicate problem: %s", body)
	}
}

func TestObjectsAuthz(t *testing.T) {
	ta := bootAPI(t)
	id, _ := ta.createHost("web01", "/")

	t.Run("read-only token may GET", func(t *testing.T) {
		if code, body := ta.read("GET", "/api/v1/objects/"+id, nil); code != 200 {
			t.Fatalf("reader GET: %d %s", code, body)
		}
		if code, body := ta.read("GET", "/api/v1/hosts", nil); code != 200 {
			t.Fatalf("reader list: %d %s", code, body)
		}
	})

	t.Run("read-only token cannot create", func(t *testing.T) {
		code, body := ta.read("POST", "/api/v1/hosts", map[string]any{
			"name": "nope", "spec": map[string]any{"checkCommand": "passive"}})
		if code != http.StatusForbidden {
			t.Fatalf("reader create: want 403, got %d %s", code, body)
		}
		p := decodeProblem(t, body)
		if p.Code != "np:auth/forbidden" || p.Detail != "objects:write" {
			t.Fatalf("bad forbidden problem: %s", body)
		}
	})

	t.Run("read-only token cannot update or delete", func(t *testing.T) {
		if code, _ := ta.read("PUT", "/api/v1/objects/"+id,
			map[string]any{"spec": map[string]any{"checkCommand": "passive"}}, ifMatch(1)); code != http.StatusForbidden {
			t.Fatalf("reader PUT: want 403, got %d", code)
		}
		if code, _ := ta.read("DELETE", "/api/v1/objects/"+id, nil); code != http.StatusForbidden {
			t.Fatalf("reader DELETE: want 403, got %d", code)
		}
	})

	t.Run("unrelated-scope token forbidden", func(t *testing.T) {
		code, body := ta.do("GET", "/api/v1/objects/"+id, nil, bearer(ta.noneToken))
		if code != http.StatusForbidden {
			t.Fatalf("outsider GET: want 403, got %d %s", code, body)
		}
	})

	t.Run("anonymous unauthorized", func(t *testing.T) {
		code, body := ta.do("GET", "/api/v1/objects/"+id, nil)
		if code != http.StatusUnauthorized {
			t.Fatalf("anon GET: want 401, got %d %s", code, body)
		}
		if p := decodeProblem(t, body); p.Code != "np:auth/required" {
			t.Fatalf("bad 401 problem: %s", body)
		}
	})
}

func TestObjectsBatch(t *testing.T) {
	ta := bootAPI(t)

	// all-or-nothing: one bad service (unknown host) rolls back everything.
	code, body := ta.admin("POST", "/api/v1/objects:batch", map[string]any{
		"hosts": []map[string]any{
			{"name": "h1", "spec": map[string]any{"checkCommand": "passive"}}},
		"services": []map[string]any{
			{"name": "s1", "host": "nope", "spec": map[string]any{"checkCommand": "passive"}}},
	})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("atomic batch with bad service: want 422, got %d %s", code, body)
	}
	// rollback: h1 must not survive.
	if c, _ := ta.admin("GET", "/api/v1/hosts", nil); c == 200 {
		var l struct{ Items []any }
		_, b := ta.admin("GET", "/api/v1/hosts", nil)
		mustJSON(t, b, &l)
		if len(l.Items) != 0 {
			t.Fatalf("atomic rollback failed, %d hosts remain", len(l.Items))
		}
	}

	// happy path: two hosts + one service created.
	code, body = ta.admin("POST", "/api/v1/objects:batch", map[string]any{
		"hosts": []map[string]any{
			{"name": "h1", "spec": map[string]any{"checkCommand": "passive"}},
			{"name": "h2", "spec": map[string]any{"checkCommand": "passive"}}},
		"services": []map[string]any{
			{"name": "s1", "host": "h1", "spec": map[string]any{"checkCommand": "passive"}}},
	})
	if code != 200 {
		t.Fatalf("good batch: %d %s", code, body)
	}
	var res struct {
		Created int `json:"created"`
		Failed  int `json:"failed"`
	}
	mustJSON(t, body, &res)
	if res.Created != 3 || res.Failed != 0 {
		t.Fatalf("batch created=%d failed=%d, want 3/0: %s", res.Created, res.Failed, body)
	}
}

func TestObjectsBatchPartialMode(t *testing.T) {
	ta := bootAPI(t)
	// partial mode keeps the good rows and reports the bad one instead of
	// rolling everything back.
	code, body := ta.admin("POST", "/api/v1/objects:batch", map[string]any{
		"mode": "partial",
		"hosts": []map[string]any{
			{"name": "ok1", "spec": map[string]any{"checkCommand": "passive"}}},
		"services": []map[string]any{
			{"name": "bad", "host": "nope", "spec": map[string]any{"checkCommand": "passive"}}},
	})
	if code != 200 {
		t.Fatalf("partial batch: %d %s", code, body)
	}
	var res struct {
		Created int `json:"created"`
		Failed  int `json:"failed"`
	}
	mustJSON(t, body, &res)
	if res.Created != 1 || res.Failed != 1 {
		t.Fatalf("partial batch created=%d failed=%d, want 1/1: %s", res.Created, res.Failed, body)
	}
	// the good host survived (no rollback in partial mode).
	_, lb := ta.admin("GET", "/api/v1/hosts", nil)
	var list struct{ Items []any }
	mustJSON(t, lb, &list)
	if len(list.Items) != 1 {
		t.Fatalf("partial mode should keep the good host, got %d", len(list.Items))
	}
}
