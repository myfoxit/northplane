package api

import (
	"encoding/json"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/northplane/northplane/internal/auth"
	"github.com/northplane/northplane/internal/metrics"
)

// OpenAPI 3.1 generation from the route registry + Go types (ADR-10:
// the code is the single source of truth; drift is impossible).

func (a *API) registerOpenAPI() {
	var cached []byte
	a.mux.HandleFunc("GET /api/openapi.json", func(w http.ResponseWriter, r *http.Request) {
		if cached == nil {
			doc := a.buildOpenAPI()
			cached, _ = json.MarshalIndent(doc, "", "  ")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(cached)
	})
	// lightweight built-in docs UI (SPEC §11.1: eingebaute Doc-UI)
	a.mux.HandleFunc("GET /api/docs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(docsHTML))
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
		okCode := "200"
		if rt.Method == "POST" && strings.HasSuffix(rt.Pattern, "s") &&
			!strings.Contains(rt.Pattern, ":") {
			okCode = "201"
		}
		if rt.Resp != nil {
			responses[okCode] = map[string]any{
				"description": "OK",
				"content": map[string]any{"application/json": map[string]any{
					"schema": schemaRef(rt.Resp, schemas)}},
			}
		} else {
			responses[okCode] = map[string]any{"description": "OK"}
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
		"servers": []map[string]any{{"url": "/"}},
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
	for t.Kind() == reflect.Ptr {
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
		if !strings.Contains(opts, "omitempty") && f.Type.Kind() != reflect.Ptr {
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

const docsHTML = `<!doctype html><html><head><meta charset="utf-8">
<title>Northplane API</title>
<meta name="viewport" content="width=device-width,initial-scale=1">
<style>
:root{color-scheme:dark}
body{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;background:#0b1220;color:#cbd5e1;margin:0;padding:2rem}
h1{color:#f1f5f9;font-size:1.3rem}h2{color:#93c5fd;font-size:1rem;margin-top:2rem;text-transform:uppercase;letter-spacing:.08em}
.op{display:flex;gap:.8rem;padding:.45rem .6rem;border-bottom:1px solid #1e293b;align-items:baseline}
.m{font-weight:700;width:4.5rem;flex-shrink:0}.m.GET{color:#34d399}.m.POST{color:#60a5fa}.m.PUT{color:#fbbf24}.m.DELETE{color:#f87171}
.p{color:#e2e8f0}.s{color:#64748b;margin-left:auto;text-align:right}
a{color:#93c5fd}
.perm{color:#a78bfa;font-size:.75rem}
</style></head><body>
<h1>Northplane API <a href="/api/openapi.json">openapi.json</a></h1>
<div id="ops">loading…</div>
<script>
fetch('/api/openapi.json').then(r=>r.json()).then(doc=>{
  const byTag={};
  for(const [path,ops] of Object.entries(doc.paths))
    for(const [method,op] of Object.entries(ops)){
      const tag=(op.tags&&op.tags[0])||'misc';
      (byTag[tag]=byTag[tag]||[]).push({method:method.toUpperCase(),path,op});
    }
  const el=document.getElementById('ops');el.innerHTML='';
  for(const tag of Object.keys(byTag).sort()){
    const h=document.createElement('h2');h.textContent=tag;el.appendChild(h);
    for(const {method,path,op} of byTag[tag]){
      const d=document.createElement('div');d.className='op';
      d.innerHTML='<span class="m '+method+'">'+method+'</span><span class="p">'+path+
        (op['x-required-permission']?' <span class="perm">['+op['x-required-permission']+']</span>':'')+
        '</span><span class="s">'+(op.summary||'')+'</span>';
      el.appendChild(d);
    }
  }
});
</script></body></html>`

var _ = auth.From
