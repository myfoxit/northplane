package main

import "testing"

func TestParseName(t *testing.T) {
	cases := []struct {
		in   string
		want Names
	}{
		{"Widget", Names{
			Pascal: "Widget", PascalPlural: "Widgets", Camel: "widget",
			Kebab: "widget", KebabPlural: "widgets", Snake: "widget",
			Title: "Widget", ConstName: "KindWidget",
		}},
		{"foo", Names{
			Pascal: "Foo", PascalPlural: "Foos", Camel: "foo",
			Kebab: "foo", KebabPlural: "foos", Snake: "foo",
			Title: "Foo", ConstName: "KindFoo",
		}},
		{"foo-bar", Names{
			Pascal: "FooBar", PascalPlural: "FooBars", Camel: "fooBar",
			Kebab: "foo-bar", KebabPlural: "foo-bars", Snake: "foo_bar",
			Title: "Foo Bar", ConstName: "KindFooBar",
		}},
		{"contactGroup", Names{
			Pascal: "ContactGroup", PascalPlural: "ContactGroups", Camel: "contactGroup",
			Kebab: "contact-group", KebabPlural: "contact-groups", Snake: "contact_group",
			Title: "Contact Group", ConstName: "KindContactGroup",
		}},
		{"maintenance_window", Names{
			Pascal: "MaintenanceWindow", PascalPlural: "MaintenanceWindows", Camel: "maintenanceWindow",
			Kebab: "maintenance-window", KebabPlural: "maintenance-windows", Snake: "maintenance_window",
			Title: "Maintenance Window", ConstName: "KindMaintenanceWindow",
		}},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got := ParseName(c.in)
			if got != c.want {
				t.Errorf("ParseName(%q) =\n  %+v\nwant\n  %+v", c.in, got, c.want)
			}
		})
	}
}

// TestPluralize covers the small English ruleset the generator advertises
// in its plan (so a surprising plural is a visible test failure, not a
// silent surprise in someone's generated routes).
func TestPluralize(t *testing.T) {
	cases := map[string]string{
		"Widget":   "Widgets",
		"Policy":   "Policies", // consonant + y → ies
		"Gateway":  "Gateways", // vowel + y → s
		"Box":      "Boxes",
		"Class":    "Classes",
		"Dish":     "Dishes",
		"Batch":    "Batches",
		"Schedule": "Schedules",
	}
	for in, want := range cases {
		if got := pluralize(in); got != want {
			t.Errorf("pluralize(%q) = %q, want %q", in, got, want)
		}
	}
}
