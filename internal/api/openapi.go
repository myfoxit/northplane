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

// docsHTML is the built-in API reference (SPEC §11.1): grouped, filter-
// able operations that expand into parameters, request/response schema
// trees (resolved from components), a copyable curl line and a try-it
// console (session cookie or pasted bearer token, kept in memory only).
// Self-contained on purpose — no CDN, works air-gapped.
const docsHTML = `<!doctype html><html><head><meta charset="utf-8">
<title>Northplane API</title>
<meta name="viewport" content="width=device-width,initial-scale=1">
<style>
:root{color-scheme:dark}
body{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;background:#0b1220;color:#cbd5e1;margin:0;padding:2rem;max-width:72rem}
h1{color:#f1f5f9;font-size:1.3rem;display:flex;align-items:baseline;gap:1rem;flex-wrap:wrap}
h1 small{color:#64748b;font-size:.8rem;font-weight:400}
h2{color:#93c5fd;font-size:1rem;margin-top:2rem;text-transform:uppercase;letter-spacing:.08em}
.bar{display:flex;gap:.8rem;margin:1rem 0;flex-wrap:wrap}
input,textarea,button{font:inherit;color:#e2e8f0;background:#111a2e;border:1px solid #1e293b;border-radius:6px;padding:.35rem .6rem}
input:focus,textarea:focus{outline:1px solid #3b82f6}
#filter{flex:1;min-width:14rem}#token{min-width:22rem}
button{cursor:pointer;background:#1d4ed8;border-color:#1d4ed8;color:#fff}button:hover{background:#2563eb}
button.ghost{background:transparent;border-color:#334155;color:#94a3b8}
.op{display:flex;gap:.8rem;padding:.45rem .6rem;border-bottom:1px solid #1e293b;align-items:baseline;cursor:pointer}
.op:hover{background:#0f172a}
.m{font-weight:700;width:4.5rem;flex-shrink:0}.m.GET{color:#34d399}.m.POST{color:#60a5fa}.m.PUT{color:#fbbf24}.m.DELETE{color:#f87171}
.p{color:#e2e8f0}.s{color:#64748b;margin-left:auto;text-align:right}
a{color:#93c5fd}
.perm{color:#a78bfa;font-size:.75rem}
.detail{display:none;border-bottom:1px solid #1e293b;background:#0d1526;padding:.8rem 1rem 1rem;font-size:.8rem}
.detail.open{display:block}
.detail h3{color:#94a3b8;font-size:.72rem;text-transform:uppercase;letter-spacing:.08em;margin:.9rem 0 .3rem}
pre{background:#0b1220;border:1px solid #1e293b;border-radius:6px;padding:.6rem;overflow:auto;margin:.2rem 0;white-space:pre-wrap}
.tree{color:#94a3b8;line-height:1.5}
.tree b{color:#e2e8f0;font-weight:600}.tree i{color:#34d399;font-style:normal}.tree em{color:#f87171;font-style:normal}
.try{display:flex;flex-direction:column;gap:.4rem;margin-top:.3rem}
.try .row{display:flex;gap:.4rem;flex-wrap:wrap;align-items:center}
.try textarea{width:100%;min-height:6rem;box-sizing:border-box}
.status-ok{color:#34d399}.status-err{color:#f87171}
</style></head><body>
<h1>Northplane API <small id="ver"></small> <a href="/api/openapi.json">openapi.json</a></h1>
<div class="bar">
  <input id="filter" placeholder="Filter (Pfad, Beschreibung, Tag) …" autofocus>
  <input id="token" placeholder="Bearer-Token (optional — Session-Cookie wird sonst genutzt)" type="password" autocomplete="off">
</div>
<div id="ops">loading…</div>
<script>
'use strict';
let SCHEMAS={};
const resolve=s=>{ if(!s)return null; if(s.$ref)return SCHEMAS[s.$ref.split('/').pop()]||null; return s; };
function shape(s,depth,seen){
  s=resolve(s); if(!s)return '';
  if(depth>4)return '…';
  if(s.type==='array')return '['+shape(s.items,depth+1,seen)+']';
  if(s.type==='object'&&s.properties){
    const req=new Set(s.required||[]);
    const rows=Object.keys(s.properties).sort().map(k=>{
      const v=s.properties[k];
      return '  '.repeat(depth)+'<b>'+k+'</b>'+(req.has(k)?'<em>*</em>':'')+': '+shape(v,depth+1,seen);
    });
    return '{\n'+rows.join('\n')+'\n'+'  '.repeat(depth-1)+'}';
  }
  if(s.type==='object')return '{…}';
  return '<i>'+(s.format||s.type||'any')+'</i>';
}
function example(s,depth){
  s=resolve(s); if(!s||depth>4)return null;
  if(s.type==='array'){const e=example(s.items,depth+1);return e===null?[]:[e];}
  if(s.type==='object'&&s.properties){
    const o={};
    for(const k of Object.keys(s.properties).sort()){
      const e=example(s.properties[k],depth+1);
      if(e!==null)o[k]=e;
    }
    return o;
  }
  if(s.type==='object')return {};
  switch(s.type){
    case 'string':return s.format==='date-time'?new Date().toISOString():'';
    case 'integer':case 'number':return 0;
    case 'boolean':return false;
    default:return null;
  }
}
function reqSchema(op){const c=op.requestBody&&op.requestBody.content;return c&&c['application/json']&&c['application/json'].schema;}
function respSchema(op){
  for(const code of Object.keys(op.responses||{}).sort())
    if(code.startsWith('2')){const c=op.responses[code].content;return c&&c['application/json']&&c['application/json'].schema;}
  return null;
}
function curl(method,path,op){
  let c='curl -H "Authorization: Bearer np_…"';
  if(method!=='GET')c+=' -X '+method;
  const rs=reqSchema(op);
  if(rs)c+=" -H 'Content-Type: application/json' -d '"+JSON.stringify(example(rs,1))+"'";
  return c+" '"+location.origin+path.replace(/\{([^}]+)\}/g,'<$1>')+"'";
}
function detailEl(method,path,op){
  const d=document.createElement('div');d.className='detail';
  let h='';
  if(op['x-required-permission'])h+='<div class="perm">Benötigte Berechtigung: '+op['x-required-permission']+'</div>';
  const params=(op.parameters||[]).map(p=>p.name);
  const rs=reqSchema(op),resp=respSchema(op);
  if(rs)h+='<h3>Request-Body</h3><pre class="tree">'+shape(rs,1)+'</pre>';
  if(resp)h+='<h3>Response</h3><pre class="tree">'+shape(resp,1)+'</pre>';
  h+='<h3>curl</h3><pre>'+curl(method,path,op).replace(/&/g,'&amp;').replace(/</g,'&lt;')+'</pre>';
  h+='<h3>Ausprobieren</h3>';
  d.innerHTML=h;
  const t=document.createElement('div');t.className='try';
  const row=document.createElement('div');row.className='row';
  const inputs={};
  for(const p of params){
    const i=document.createElement('input');i.placeholder='{'+p+'}';inputs[p]=i;row.appendChild(i);
  }
  const q=document.createElement('input');q.placeholder='?query=…';row.appendChild(q);
  const send=document.createElement('button');send.textContent=method+' senden';row.appendChild(send);
  const st=document.createElement('span');row.appendChild(st);
  t.appendChild(row);
  let body=null;
  if(rs){
    body=document.createElement('textarea');
    body.value=JSON.stringify(example(rs,1),null,2);
    t.appendChild(body);
  }
  const out=document.createElement('pre');out.style.display='none';t.appendChild(out);
  send.onclick=async()=>{
    let url=path;
    for(const p of params)url=url.replace('{'+p+'}',encodeURIComponent(inputs[p].value));
    url+=q.value?(q.value.startsWith('?')?q.value:'?'+q.value):'';
    const headers={};
    const tok=document.getElementById('token').value.trim();
    if(tok)headers['Authorization']='Bearer '+tok;
    const init={method,headers,credentials:'same-origin'};
    if(body&&body.value.trim()){headers['Content-Type']='application/json';init.body=body.value;}
    st.textContent='…';st.className='';
    try{
      const r=await fetch(url,init);
      st.textContent='HTTP '+r.status;st.className=r.ok?'status-ok':'status-err';
      const text=await r.text();
      out.style.display='block';
      try{out.textContent=JSON.stringify(JSON.parse(text),null,2);}catch(e){out.textContent=text.slice(0,8192);}
    }catch(e){st.textContent=String(e);st.className='status-err';}
  };
  d.appendChild(t);
  return d;
}
fetch('/api/openapi.json').then(r=>r.json()).then(doc=>{
  SCHEMAS=(doc.components&&doc.components.schemas)||{};
  document.getElementById('ver').textContent=doc.info&&doc.info.version||'';
  const byTag={};
  for(const [path,ops] of Object.entries(doc.paths))
    for(const [method,op] of Object.entries(ops)){
      const tag=(op.tags&&op.tags[0])||'misc';
      (byTag[tag]=byTag[tag]||[]).push({method:method.toUpperCase(),path,op});
    }
  const el=document.getElementById('ops');el.innerHTML='';
  const sections=[];
  for(const tag of Object.keys(byTag).sort()){
    const h=document.createElement('h2');h.textContent=tag;el.appendChild(h);
    const rows=[];
    for(const {method,path,op} of byTag[tag]){
      const d=document.createElement('div');d.className='op';
      d.innerHTML='<span class="m '+method+'">'+method+'</span><span class="p">'+path+
        (op['x-required-permission']?' <span class="perm">['+op['x-required-permission']+']</span>':'')+
        '</span><span class="s">'+(op.summary||'')+'</span>';
      el.appendChild(d);
      let detail=null;
      d.onclick=()=>{
        if(!detail){detail=detailEl(method,path,op);d.after(detail);}
        detail.classList.toggle('open');
      };
      rows.push({el:d,getDetail:()=>detail,hay:(method+' '+path+' '+(op.summary||'')+' '+tag).toLowerCase()});
    }
    sections.push({header:h,rows});
  }
  document.getElementById('filter').oninput=e=>{
    const needle=e.target.value.toLowerCase();
    for(const sec of sections){
      let any=false;
      for(const r of sec.rows){
        const hit=!needle||r.hay.includes(needle);
        r.el.style.display=hit?'':'none';
        const det=r.getDetail();if(det&&!hit)det.classList.remove('open');
        any=any||hit;
      }
      sec.header.style.display=any?'':'none';
    }
  };
});
</script></body></html>`

var _ = auth.From
