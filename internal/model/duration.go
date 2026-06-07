package model

import (
	"encoding/json"
	"fmt"
	"time"
)

// Duration is a time.Duration that marshals as Go duration syntax
// ("90s", "5m", "24h") in JSON and YAML — SPEC §11.1.
type Duration time.Duration

func (d Duration) D() time.Duration { return time.Duration(d) }

func (d Duration) String() string { return time.Duration(d).String() }

func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", b, err)
	}
	*d = Duration(v)
	return nil
}

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

func (d *Duration) UnmarshalJSON(b []byte) error {
	// Accept both "60s" and bare integer seconds (lenient ingestion).
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		return d.UnmarshalText([]byte(s))
	}
	var n int64
	if err := json.Unmarshal(b, &n); err == nil {
		*d = Duration(time.Duration(n) * time.Second)
		return nil
	}
	return fmt.Errorf("invalid duration %s", b)
}

// MarshalYAML/UnmarshalYAML satisfy gopkg.in/yaml.v3 via the text interfaces;
// yaml.v3 honours encoding.TextMarshaler/TextUnmarshaler automatically.

// Time helpers: the whole system speaks RFC 3339 UTC (SPEC §11.1).

const TimeFormat = time.RFC3339Nano

// Now returns the current UTC time truncated to millisecond precision —
// the canonical persisted resolution.
func Now() time.Time { return time.Now().UTC().Truncate(time.Millisecond) }

// FormatTime renders t in the canonical wire format.
func FormatTime(t time.Time) string { return t.UTC().Format(TimeFormat) }

// ParseTime parses canonical wire timestamps.
func ParseTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}
