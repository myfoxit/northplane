package nagios

import "testing"

// TestParseRangeTilde guards the "~" end-bound regression: "~:~" used to
// resolve to [-Inf,-Inf] and so alerted on every value.
func TestParseRangeTilde(t *testing.T) {
	r, err := ParseRange("~:~")
	if err != nil {
		t.Fatalf("parse ~:~: %v", err)
	}
	if r.Violated(42) {
		t.Fatalf("~:~ must never violate, but flagged 42")
	}
	// "~:10" = alert when > 10 (start unbounded below).
	r2, _ := ParseRange("~:10")
	if r2.Violated(5) || !r2.Violated(11) {
		t.Fatalf("~:10 semantics wrong: 5=%v 11=%v", r2.Violated(5), r2.Violated(11))
	}
}

// TestParsePerfNonFinite guards rejection of NaN/Inf perfdata values that
// would otherwise poison TSDB aggregation and break JSON encoding.
func TestParsePerfNonFinite(t *testing.T) {
	for _, in := range []string{"x=NaN", "x=Inf", "x=-Inf", "x=1e400"} {
		if perfs, _ := ParsePerfdata(in); len(perfs) != 0 {
			t.Fatalf("%q should be rejected, got %+v", in, perfs)
		}
	}
	// a normal value still parses
	if perfs, _ := ParsePerfdata("rtt=12.5ms"); len(perfs) != 1 || perfs[0].Value != 12.5 {
		t.Fatalf("normal perfdata broke: %+v", perfs)
	}
}
