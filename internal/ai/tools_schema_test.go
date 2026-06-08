package ai

import (
	"bytes"
	"encoding/json"
	"testing"
)

// jsonSchemaShape is the subset of a tool input schema this test inspects.
type jsonSchemaShape struct {
	Type       string `json:"type"`
	Properties map[string]struct {
		Type        string   `json:"type"`
		Description string   `json:"description"`
		Enum        []string `json:"enum"`
	} `json:"properties"`
	Required []string `json:"required"`
}

// TestToolSchemasWellFormed guards the schema/impl seam now that schemas are
// reflected from the typed input structs the Run closures unmarshal into.
// It asserts, for every tool, that the declared schema is a well-formed
// object schema: every property carries a known JSON-Schema scalar type and
// a non-empty description, every `required` entry names a real property, and
// every enum is non-empty with string values. A struct field that loses its
// `desc`/`json` tag, or a `required` field that drifts from the struct,
// surfaces here instead of silently shipping a degraded tool surface.
func TestToolSchemasWellFormed(t *testing.T) {
	validTypes := map[string]bool{
		"string": true, "integer": true, "number": true, "boolean": true,
	}
	for _, tool := range buildTools() {
		t.Run(tool.Def.Name, func(t *testing.T) {
			var sh jsonSchemaShape
			if err := json.Unmarshal(tool.Def.Schema, &sh); err != nil {
				t.Fatalf("schema is not valid JSON: %v\n%s", err, tool.Def.Schema)
			}
			if sh.Type != "object" {
				t.Errorf("top-level type = %q, want object", sh.Type)
			}
			for name, prop := range sh.Properties {
				if !validTypes[prop.Type] {
					t.Errorf("property %q has unknown type %q", name, prop.Type)
				}
				if prop.Description == "" {
					t.Errorf("property %q is missing a description", name)
				}
				if prop.Enum != nil {
					if len(prop.Enum) == 0 {
						t.Errorf("property %q declares an empty enum", name)
					}
					for _, e := range prop.Enum {
						if e == "" {
							t.Errorf("property %q has an empty enum value", name)
						}
					}
				}
			}
			for _, req := range sh.Required {
				if _, ok := sh.Properties[req]; !ok {
					t.Errorf("required %q is not a declared property", req)
				}
			}
		})
	}
}

// TestToolSchemaRoundTripsInputStruct proves the schema and the parsing
// share one source of truth: a JSON object built from the schema's declared
// properties unmarshals cleanly into nothing left over — i.e. the property
// names the schema advertises are exactly the json keys the input struct
// accepts (DisallowUnknownFields would reject a drifted name). This is the
// drift guard the task asks for: schema properties == fields the Run closure
// reads, because both derive from the same struct.
func TestToolSchemaRoundTripsInputStruct(t *testing.T) {
	// Map each tool to a fresh pointer of its input struct. Kept in sync with
	// buildTools by the schema-equality assertion below.
	inputs := map[string]any{
		"get_overview":          &emptyInput{},
		"search_objects":        &searchObjectsInput{},
		"get_object":            &getObjectInput{},
		"query_metrics":         &queryMetricsInput{},
		"get_alerts":            &getAlertsInput{},
		"analyze_metric":        &analyzeMetricInput{},
		"forecast_capacity":     &forecastCapacityInput{},
		"suggest_thresholds":    &suggestThresholdsInput{},
		"get_incidents":         &getIncidentsInput{},
		"who_is_oncall":         &whoIsOncallInput{},
		"explain_alert":         &explainAlertInput{},
		"run_check_now":         &runCheckNowInput{},
		"acknowledge_alert":     &acknowledgeAlertInput{},
		"create_downtime":       &createDowntimeInput{},
		"create_silence":        &createSilenceInput{},
		"propose_config_change": &proposeConfigChangeInput{},
		"apply_config_change":   &applyConfigChangeInput{},
		"render_report":         &renderReportInput{},
	}
	for _, tool := range buildTools() {
		t.Run(tool.Def.Name, func(t *testing.T) {
			proto, ok := inputs[tool.Def.Name]
			if !ok {
				t.Fatalf("no input struct registered for %s — update this test", tool.Def.Name)
			}
			// The declared schema must equal what the input struct reflects:
			// any divergence means a Run closure unmarshals into a different
			// struct than the one the schema is built from.
			if got, want := string(tool.Def.Schema), string(reflectSchema(proto)); got != want {
				t.Errorf("schema does not match input struct:\n got:  %s\n want: %s", got, want)
			}

			var sh jsonSchemaShape
			if err := json.Unmarshal(tool.Def.Schema, &sh); err != nil {
				t.Fatal(err)
			}
			// Build a sample object using the schema's property names and feed
			// it back through DisallowUnknownFields: a property the input
			// struct does not accept (a drifted json tag) fails here.
			sample := map[string]any{}
			for name, prop := range sh.Properties {
				switch prop.Type {
				case "string":
					if len(prop.Enum) > 0 {
						sample[name] = prop.Enum[0]
					} else {
						sample[name] = "x"
					}
				case "integer", "number":
					sample[name] = 1
				case "boolean":
					sample[name] = true
				}
			}
			raw, _ := json.Marshal(sample)
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.DisallowUnknownFields()
			if err := dec.Decode(proto); err != nil {
				t.Errorf("schema property not accepted by input struct: %v\n%s", err, raw)
			}
		})
	}
}
