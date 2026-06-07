package model

import (
	"testing"
	"time"
)

func TestPreferredChannelsNamedPeriod(t *testing.T) {
	office := &TimePeriod{Name: "office", Days: map[string][]string{
		"monday": {"09:00-17:00"}, "tuesday": {"09:00-17:00"},
		"wednesday": {"09:00-17:00"}, "thursday": {"09:00-17:00"},
		"friday": {"09:00-17:00"},
	}}
	lookup := func(name string) *TimePeriod {
		if name == "office" {
			return office
		}
		return nil
	}
	c := &Contact{
		TimeZone: "UTC",
		Preferences: []ChannelPreference{
			{Profile: "office", Period: "office", Channels: []ChannelType{ChannelTeams}},
			{Profile: "default", Channels: []ChannelType{ChannelEmail}},
		},
	}

	// Wednesday 10:00 UTC → office period matches → teams.
	wed := time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC)
	if got := PreferredChannels(c, SevCritical, wed, lookup); len(got) != 1 || got[0] != ChannelTeams {
		t.Fatalf("office hours: got %v, want [teams]", got)
	}
	// Wednesday 22:00 UTC → outside office → fallback email.
	night := time.Date(2026, 6, 3, 22, 0, 0, 0, time.UTC)
	if got := PreferredChannels(c, SevCritical, night, lookup); len(got) != 1 || got[0] != ChannelEmail {
		t.Fatalf("night fallback: got %v, want [email]", got)
	}
	// Stored period wins over the builtin profile of the same name:
	// a user-defined "worktime" that is closed on Wednesday must NOT
	// match even though the builtin worktime profile would.
	closed := &TimePeriod{Name: "worktime", Days: map[string][]string{"saturday": {"09:00-17:00"}}}
	c2 := &Contact{Preferences: []ChannelPreference{
		{Period: "worktime", Channels: []ChannelType{ChannelSMS}},
		{Channels: []ChannelType{ChannelEmail}},
	}}
	got := PreferredChannels(c2, SevCritical, wed, func(string) *TimePeriod { return closed })
	if len(got) != 1 || got[0] != ChannelEmail {
		t.Fatalf("stored period precedence: got %v, want [email]", got)
	}
}

func TestPreferredChannelsSeverityFilter(t *testing.T) {
	c := &Contact{Preferences: []ChannelPreference{
		{Severity: SevCritical, Channels: []ChannelType{ChannelSMS}},
		{Channels: []ChannelType{ChannelEmail}},
	}}
	// Critical alert → first matching preference without period = SMS pref
	// is period-less, so it becomes the fallback before email is reached.
	if got := PreferredChannels(c, SevCritical, time.Now(), nil); len(got) != 1 || got[0] != ChannelSMS {
		t.Fatalf("critical: got %v, want [sms]", got)
	}
	// Warning is below the SMS preference's minimum → email.
	if got := PreferredChannels(c, SevWarning, time.Now(), nil); len(got) != 1 || got[0] != ChannelEmail {
		t.Fatalf("warning: got %v, want [email]", got)
	}
}

func TestNotifyTokenAndWantsNotify(t *testing.T) {
	cases := []struct {
		state State
		kind  Kind
		want  string
	}{
		{StateOK, KindService, "recovery"},
		{StateWarning, KindService, "warning"},
		{StateCritical, KindService, "critical"},
		{StateUnknown, KindService, "unknown"},
		{HostUp, KindHost, "recovery"},
		{HostDown, KindHost, "down"},
		{HostUnreachable, KindHost, "unreachable"},
	}
	for _, tc := range cases {
		if got := NotifyToken(tc.state, tc.kind); got != tc.want {
			t.Errorf("NotifyToken(%d,%s) = %q, want %q", tc.state, tc.kind, got, tc.want)
		}
	}

	empty := &ObjectSpec{}
	if !empty.WantsNotify("critical") || !empty.WantsNotify("recovery") {
		t.Fatal("empty notifyOn must match everything")
	}
	filtered := &ObjectSpec{NotifyOn: []string{"critical", "recovery"}}
	if !filtered.WantsNotify("critical") || filtered.WantsNotify("warning") {
		t.Fatal("notifyOn filter not applied")
	}
}

func TestMergeSpecNotificationRouting(t *testing.T) {
	base := ObjectSpec{
		ContactGroups: []string{"ops"},
		NotifyOn:      []string{"critical"},
	}
	child := ObjectSpec{ContactGroups: []string{"dba"}}
	out := MergeSpec(base, child)
	if len(out.ContactGroups) != 1 || out.ContactGroups[0] != "dba" {
		t.Fatalf("child contactGroups must win: %v", out.ContactGroups)
	}
	if len(out.NotifyOn) != 1 || out.NotifyOn[0] != "critical" {
		t.Fatalf("notifyOn must inherit: %v", out.NotifyOn)
	}
	if len(MergeSpec(base, ObjectSpec{}).ContactGroups) != 1 {
		t.Fatal("empty child must inherit contactGroups")
	}
}
