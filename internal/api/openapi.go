package api

import (
	"embed"
	"encoding/json"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/northplane/northplane/internal/auth"
	"github.com/northplane/northplane/internal/metrics"
)

// OpenAPI 3.1 generation from the route registry + Go types (ADR-10:
// the code is the single source of truth; drift is impossible).

//go:embed swaggerui
var swaggerFS embed.FS

func (a *API) registerOpenAPI() {
	var cached []byte
	a.mux.HandleFunc("GET /api/openapi.json", func(w http.ResponseWriter, r *http.Request) {
		if cached == nil {
			doc := a.buildOpenAPI()
			cached, _ = json.MarshalIndent(doc, "", "  ")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(cached)
	})
	// Built-in docs UI (SPEC §11.1): standard Swagger UI, vendored into
	// the binary (internal/api/swaggerui, Apache-2.0) so it works
	// air-gapped. no-cache everywhere: the page must follow the binary,
	// a stale cached shell otherwise keeps dead behaviour alive.
	a.mux.HandleFunc("GET /api/docs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write([]byte(docsHTML))
	})
	a.mux.HandleFunc("GET /api/docs/{asset}", func(w http.ResponseWriter, r *http.Request) {
		asset := r.PathValue("asset")
		body, err := swaggerFS.ReadFile("swaggerui/" + asset)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		switch {
		case strings.HasSuffix(asset, ".js"):
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		case strings.HasSuffix(asset, ".css"):
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
		default:
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		}
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(body)
	})
}

// OpenAPIDocument builds the same OpenAPI 3.1 document the server serves
// at GET /api/openapi.json, but without any running dependencies. Route
// registration only records metadata and installs handler closures (the
// Store/Bus/… fields are dereferenced solely at request time), so a bare
// API can enumerate every route and reflect every domain type. The CLI
// (`northplaned openapi`) uses this for the typed-codegen pipeline so the
// frontend's generated types can never silently drift from the Go API.
func OpenAPIDocument(version string) map[string]any {
	// Metrics is the one dependency touched at registration time: registerSystem
	// calls Metrics.Collect to register a scrape-time collector callback (the
	// callback body — which reads Bus/Hub/Sched/… — only runs on /metrics, never
	// here). A fresh registry makes that call a harmless no-op, leaving every
	// other dependency nil and untouched.
	a := &API{Version: version, mux: http.NewServeMux(), Metrics: metrics.NewRegistry()}
	a.registerAll()
	return a.buildOpenAPI()
}

func (a *API) buildOpenAPI() map[string]any {
	schemas := map[string]any{}
	paths := map[string]map[string]any{}

	routes := append([]routeMeta{}, a.routes...)
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Pattern != routes[j].Pattern {
			return routes[i].Pattern < routes[j].Pattern
		}
		return routes[i].Method < routes[j].Method
	})

	for _, rt := range routes {
		oaPath := muxToOpenAPI(rt.Pattern)
		if paths[oaPath] == nil {
			paths[oaPath] = map[string]any{}
		}
		op := map[string]any{
			"summary":     rt.Summary,
			"tags":        []string{rt.Tag},
			"operationId": opID(rt),
		}
		if rt.Perm != "" {
			op["security"] = []map[string]any{{"bearerToken": []string{}}}
			op["x-required-permission"] = string(rt.Perm)
		}
		var params []map[string]any
		for _, seg := range strings.Split(rt.Pattern, "/") {
			if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
				name := strings.Trim(seg, "{}")
				params = append(params, map[string]any{
					"name": name, "in": "path", "required": true,
					"schema": map[string]any{"type": "string"},
				})
			}
		}
		for _, q := range queryParams(rt) {
			typ := q.Type
			if typ == "" {
				typ = "string"
			}
			p := map[string]any{
				"name": q.Name, "in": "query", "required": false,
				"schema": map[string]any{"type": typ},
			}
			if q.Desc != "" {
				p["description"] = q.Desc
			}
			params = append(params, p)
		}
		if len(params) > 0 {
			op["parameters"] = params
		}
		if rt.Req != nil {
			op["requestBody"] = map[string]any{
				"content": map[string]any{"application/json": map[string]any{
					"schema": schemaRef(rt.Req, schemas)}},
			}
		}
		responses := map[string]any{
			"default": map[string]any{
				"description": "Problem Details (RFC 9457)",
				"content": map[string]any{"application/problem+json": map[string]any{
					"schema": schemaRef(reflect.TypeOf(problemDoc{}), schemas)}},
			},
		}
		okCode := successStatus(rt)
		if rt.Resp != nil && okCode != "204" {
			responses[okCode] = map[string]any{
				"description": statusText(okCode),
				"content": map[string]any{"application/json": map[string]any{
					"schema": schemaRef(rt.Resp, schemas)}},
			}
		} else {
			responses[okCode] = map[string]any{"description": statusText(okCode)}
		}
		op["responses"] = responses
		paths[oaPath][strings.ToLower(rt.Method)] = op
	}

	return map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{
			"title":       "Northplane API",
			"version":     a.Version,
			"description": "API-first monitoring & alerting. Every UI/CLI/AI capability exists here first (SPEC P1).",
		},
		// Document-level security: every operation accepts a bearer token
		// (or the session cookie), including the ones without a specific
		// permission — so Authorize in the docs UI applies everywhere.
		"security": []map[string]any{{"bearerToken": []string{}}},
		"servers":  []map[string]any{{"url": "/"}},
		"components": map[string]any{
			"schemas": schemas,
			"securitySchemes": map[string]any{
				"bearerToken": map[string]any{
					"type": "http", "scheme": "bearer",
					"description": "API token (np_…) or session cookie for the embedded UI",
				},
			},
		},
		"paths": paths,
	}
}

// queryParams returns the documented query parameters for a route: the
// explicitly-declared ones, plus cursor-pagination params auto-added for
// list endpoints (those returning the listResponse envelope), de-duplicated
// so an explicit declaration wins.
func queryParams(rt routeMeta) []oaParam {
	out := append([]oaParam{}, rt.Query...)
	seen := map[string]bool{}
	for _, q := range out {
		seen[q.Name] = true
	}
	if rt.Resp == reflect.TypeOf(listResponse{}) {
		for _, q := range []oaParam{
			{Name: "cursor", Desc: "Opaque pagination cursor from a prior response's nextCursor", Type: "string"},
			{Name: "limit", Desc: "Maximum items to return in this page", Type: "integer"},
		} {
			if !seen[q.Name] {
				out = append(out, q)
			}
		}
	}
	return out
}

// successStatus picks the documented success status code. An explicit
// override (Status(...)) wins; otherwise a method/shape heuristic far more
// accurate than the old "POST + path ends in s → 201":
//   - DELETE → 204 (the codebase's no-content delete convention)
//   - POST that creates a top-level collection member (no path variable
//     anywhere, no :action) → 201
//   - any other POST (sub-action on a resource, {id}:action) → 200
//   - everything else → 200
//
// Async POSTs that return 202 declare it explicitly via Status(202).
func successStatus(rt routeMeta) string {
	if rt.SuccessStatus != 0 {
		return strconv.Itoa(rt.SuccessStatus)
	}
	switch rt.Method {
	case http.MethodDelete:
		return "204"
	case http.MethodPost:
		// Only a POST to a pure collection (no {param} in the whole path and
		// no :action) creates a resource → 201. POSTs under a specific
		// resource (e.g. /objects/{id}/check-now) are actions → 200.
		if !strings.Contains(rt.Pattern, "{") && !strings.Contains(rt.Pattern, ":") {
			return "201"
		}
		return "200"
	default:
		return "200"
	}
}

func statusText(code string) string {
	switch code {
	case "201":
		return "Created"
	case "202":
		return "Accepted"
	case "204":
		return "No Content"
	default:
		return "OK"
	}
}

func opID(rt routeMeta) string {
	id := strings.ToLower(rt.Method) + rt.Pattern
	id = strings.NewReplacer("/api/v1/", "_", "/", "_", "{", "", "}", "", ":", "_", "-", "_").Replace(id)
	return strings.Trim(id, "_")
}

func muxToOpenAPI(pattern string) string {
	// ServeMux "{name}" syntax matches OpenAPI; just strip "{$}".
	return strings.TrimSuffix(pattern, "{$}")
}

// schemaRef reflects a Go type into components/schemas.
func schemaRef(t reflect.Type, schemas map[string]any) map[string]any {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		if t == reflect.TypeOf(time.Time{}) {
			return map[string]any{"type": "string", "format": "date-time"}
		}
		name := t.Name()
		if name == "" {
			return structSchema(t, schemas)
		}
		if _, ok := schemas[name]; !ok {
			schemas[name] = map[string]any{} // reserve against recursion
			schemas[name] = structSchema(t, schemas)
		}
		return map[string]any{"$ref": "#/components/schemas/" + name}
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return map[string]any{"type": "string", "format": "byte"}
		}
		return map[string]any{"type": "array", "items": schemaRef(t.Elem(), schemas)}
	case reflect.Map:
		return map[string]any{"type": "object",
			"additionalProperties": schemaRef(t.Elem(), schemas)}
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Interface:
		return map[string]any{}
	default:
		return map[string]any{}
	}
}

func structSchema(t reflect.Type, schemas map[string]any) map[string]any {
	props := map[string]any{}
	var required []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		if name == "" {
			if f.Anonymous {
				// embed: inline fields
				emb := schemaRef(f.Type, schemas)
				if ref, ok := emb["$ref"].(string); ok {
					refName := strings.TrimPrefix(ref, "#/components/schemas/")
					if s, ok := schemas[refName].(map[string]any); ok {
						if p, ok := s["properties"].(map[string]any); ok {
							for k, v := range p {
								props[k] = v
							}
						}
					}
					continue
				}
				if p, ok := emb["properties"].(map[string]any); ok {
					for k, v := range p {
						props[k] = v
					}
				}
				continue
			}
			name = f.Name
		}
		props[name] = schemaRef(f.Type, schemas)
		if !strings.Contains(opts, "omitempty") && f.Type.Kind() != reflect.Pointer {
			required = append(required, name)
		}
	}
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		sort.Strings(required)
		out["required"] = required
	}
	return out
}

// docsHTML mounts Swagger UI against the live OpenAPI document.
// withCredentials sends the np_session cookie on try-it requests, so a
// logged-in operator can execute calls without any token; the Authorize
// button accepts an np_… token for everyone else (not persisted).
const docsHTML = `<!doctype html><html><head><meta charset="utf-8">
<title>Northplane API</title>
<meta name="viewport" content="width=device-width,initial-scale=1">
<link rel="stylesheet" href="/api/docs/swagger-ui.css">
<style>body{margin:0}</style>
</head><body>
<div id="swagger-ui"></div>
<script src="/api/docs/swagger-ui-bundle.js"></script>
<script>
window.ui = SwaggerUIBundle({
  url: '/api/openapi.json',
  dom_id: '#swagger-ui',
  presets: [SwaggerUIBundle.presets.apis],
  layout: 'BaseLayout',
  filter: true,
  tryItOutEnabled: true,
  withCredentials: true,
  displayRequestDuration: true,
  showExtensions: true,
  operationsSorter: 'alpha',
  tagsSorter: 'alpha',
  validatorUrl: null,
});
</script></body></html>`

var _ = auth.From
