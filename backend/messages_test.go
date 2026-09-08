package main

import (
	"testing"
	"time"
)

// These messages are the entire product as far as the person receiving them
// is concerned, so the exact bytes are asserted rather than just "contains
// the word tanks". A silent reword would otherwise reach a phone unnoticed.

func TestHourLabel(t *testing.T) {
	cases := map[int]string{0: "12am", 1: "1am", 9: "9am", 11: "11am",
		12: "12pm", 13: "1pm", 14: "2pm", 23: "11pm"}
	for h, want := range cases {
		if got := hourLabel(h); got != want {
			t.Errorf("hourLabel(%d) = %q, want %q", h, got, want)
		}
	}
}

func TestDwellLabel(t *testing.T) {
	cases := map[int]string{60: "1min", 120: "2min", 180: "3min", 90: "90s", 45: "45s"}
	for s, want := range cases {
		if got := dwellLabel(s); got != want {
			t.Errorf("dwellLabel(%d) = %q, want %q", s, got, want)
		}
	}
}

func TestCompliantMessage(t *testing.T) {
	// 2026-09-07 is a Monday - the day the validation footage came from.
	seenAt := time.Date(2026, 9, 7, 12, 31, 20, 0, time.Local)
	want := "✅ Employees were seen at tanks for >2min at 12:31 PM.\n\n" +
		"Date: 07/09 Mon"
	if got := compliantMessage(seenAt, 120); got != want {
		t.Errorf("compliantMessage:\n got %q\nwant %q", got, want)
	}
}

func TestCompliantMessageTracksDwellConfig(t *testing.T) {
	// The stated duration must follow dwell_seconds, or the message would
	// claim a threshold the detector never applied.
	seenAt := time.Date(2026, 9, 7, 9, 5, 0, 0, time.Local)
	if got := compliantMessage(seenAt, 180); got != "✅ Employees were seen at tanks for >3min at 09:05 AM.\n\n"+
		"Date: 07/09 Mon" {
		t.Errorf("dwell_seconds=180 should read >3min, got %q", got)
	}
}

func TestBreachMessage(t *testing.T) {
	now := time.Date(2026, 9, 7, 14, 0, 0, 0, time.Local)
	want := "❌ Employees did NOT check or refill tanks. Monitored 9am-2pm.\n\n" +
		"Date: 07/09 Mon"
	if got := breachMessage(now, 9, 14); got != want {
		t.Errorf("breachMessage:\n got %q\nwant %q", got, want)
	}
}

func TestBreachMessageTracksScheduleConfig(t *testing.T) {
	// The window in the text is claimed as fact to whoever reads it, so it
	// has to come from the schedule actually watched, not a hardcoded string.
	now := time.Date(2026, 9, 7, 17, 0, 0, 0, time.Local)
	want := "❌ Employees did NOT check or refill tanks. Monitored 7am-5pm.\n\n" +
		"Date: 07/09 Mon"
	if got := breachMessage(now, 7, 17); got != want {
		t.Errorf("breachMessage with a 7-17 window:\n got %q\nwant %q", got, want)
	}
}

func TestProofCrop(t *testing.T) {
	cases := []struct {
		name string
		run  []string
		want string
	}{
		{"empty run sends no photo", nil, ""},
		{"single frame", []string{"a"}, "a"},
		{"two frames takes the later", []string{"a", "b"}, "b"},
		{"three frames takes the middle", []string{"a", "b", "c"}, "b"},
		{"five frames takes the middle", []string{"a", "b", "c", "d", "e"}, "c"},
		{"even run stays past the halfway point", []string{"a", "b", "c", "d"}, "c"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := proofCrop(c.run); got != c.want {
				t.Errorf("proofCrop(%v) = %q, want %q", c.run, got, c.want)
			}
		})
	}
}

func TestStringList(t *testing.T) {
	// State round-trips through JSON, so a persisted []string comes back as
	// []any. Both shapes, plus a missing key, have to decode.
	if got := stringList([]any{"a", "b"}); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("[]any should decode to []string, got %v", got)
	}
	if got := stringList([]string{"a"}); len(got) != 1 || got[0] != "a" {
		t.Errorf("[]string should pass through, got %v", got)
	}
	if got := stringList(nil); len(got) != 0 {
		t.Errorf("a missing key should decode to empty, got %v", got)
	}
	if got := stringList("not a list"); len(got) != 0 {
		t.Errorf("an unexpected type should decode to empty, got %v", got)
	}
}
