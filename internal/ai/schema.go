package ai

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// This file makes a tool's input JSON Schema and the struct its Run closure
// unmarshals into a single source of truth. Previously each tool declared a
// hand-written JSON Schema string (sch("…")) separate from an anonymous
// args struct inside Run — a drift seam where a renamed field or a new enum
// value silently diverged from the advertised schema. reflectSchema derives
// the schema from a typed struct, honouring `json` tags (names + required)
// and a small `desc` / `enum` tag vocabulary for the property descriptions
// and enums the MCP/AI surface relies on.
//
// The output shape deliberately matches what was hand-written before so the
// MCP tool surface (and its test) is unchanged:
//
//	{"type":"object","properties":{ "<name>": {"type":..,"description":..,"enum":[..]} }, "required":[..]}
//
// Field tags:
//
//	json:"name,omitempty"  → property name; omitempty/omitzero ⇒ optional.
//	desc:"…"               → property description.
//	enum:"a|b|c"           → allowed values (pipe-separated; emitted as a
//	                         JSON array of the field's scalar kind).

// schemaProp is one JSON-Schema property. Field order and omitempty are
// chosen so the marshalled object reads type→description→enum like the
// hand-written schemas did.
type schemaProp struct {
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
	Enum        []any  `json:"enum,omitempty"`
}

// objectSchema is the top-level "object" schema for a tool input.
type objectSchema struct {
	Type       string                `json:"type"`
	Properties map[string]schemaProp `json:"properties"`
	Required   []string              `json:"required,omitempty"`
	// propOrder preserves struct field order for deterministic marshalling
	// (encoding/json sorts map keys, so Properties is emitted via a custom
	// MarshalJSON that walks propOrder).
	propOrder []string
}

// MarshalJSON emits properties in struct-field order (stable, readable,
// diff-friendly) rather than encoding/json's map-key sort.
func (o objectSchema) MarshalJSON() ([]byte, error) {
	var b strings.Builder
	b.WriteString(`{"type":"object","properties":{`)
	for i, name := range o.propOrder {
		if i > 0 {
			b.WriteByte(',')
		}
		key, _ := json.Marshal(name)
		b.Write(key)
		b.WriteByte(':')
		pv, err := json.Marshal(o.Properties[name])
		if err != nil {
			return nil, err
		}
		b.Write(pv)
	}
	b.WriteByte('}')
	if len(o.Required) > 0 {
		req, _ := json.Marshal(o.Required)
		b.WriteString(`,"required":`)
		b.Write(req)
	}
	b.WriteByte('}')
	return []byte(b.String()), nil
}

// reflectSchema builds the input JSON Schema for a tool from a zero value of
// its args struct. It panics on a misconfigured struct (a programmer error
// caught at process start, since buildTools runs eagerly) rather than
// returning an error every call site would have to thread.
func reflectSchema(proto any) json.RawMessage {
	t := reflect.TypeOf(proto)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		panic(fmt.Sprintf("reflectSchema: want struct, got %s", t.Kind()))
	}
	s := objectSchema{Type: "object", Properties: map[string]schemaProp{}}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, opts := jsonName(f)
		if name == "-" {
			continue
		}
		jsType, ok := jsonSchemaType(f.Type)
		if !ok {
			panic(fmt.Sprintf("reflectSchema: field %s.%s has unsupported type %s",
				t.Name(), f.Name, f.Type))
		}
		prop := schemaProp{Type: jsType, Description: f.Tag.Get("desc")}
		if enum := f.Tag.Get("enum"); enum != "" {
			prop.Enum = parseEnum(enum)
		}
		s.Properties[name] = prop
		s.propOrder = append(s.propOrder, name)
		if !opts["omitempty"] && !opts["omitzero"] {
			s.Required = append(s.Required, name)
		}
	}
	out, err := json.Marshal(s)
	if err != nil {
		panic("reflectSchema: " + err.Error())
	}
	return out
}

// jsonName resolves a field's JSON property name and its tag options. With
// no json tag the field is camel-cased (lowerCamel) so schemas read like
// the API's JSON, matching the previously hand-written property names.
func jsonName(f reflect.StructField) (string, map[string]bool) {
	opts := map[string]bool{}
	tag := f.Tag.Get("json")
	name, rest, _ := strings.Cut(tag, ",")
	for _, o := range strings.Split(rest, ",") {
		if o != "" {
			opts[o] = true
		}
	}
	if name == "" {
		name = lowerFirst(f.Name)
	}
	return name, opts
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	if r[0] >= 'A' && r[0] <= 'Z' {
		r[0] += 'a' - 'A'
	}
	return string(r)
}

// jsonSchemaType maps a Go kind onto the JSON Schema scalar type used by the
// tool inputs. Only the scalar kinds the tools actually accept are
// supported (string/number/integer/boolean); anything else is a programmer
// error surfaced by reflectSchema's panic.
func jsonSchemaType(t reflect.Type) (string, bool) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String:
		return "string", true
	case reflect.Bool:
		return "boolean", true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "integer", true
	case reflect.Float32, reflect.Float64:
		return "number", true
	case reflect.Map:
		// free-form JSON object (generic config-resource documents)
		return "object", true
	default:
		return "", false
	}
}

// parseEnum splits "a|b|c" into a JSON array of strings. Enums in the tool
// surface are all string-valued (status, agg, kind), so values are emitted
// as strings.
func parseEnum(s string) []any {
	parts := strings.Split(s, "|")
	out := make([]any, 0, len(parts))
	for _, p := range parts {
		out = append(out, p)
	}
	return out
}
