package main

import (
	"testing"
	"time"
)

// applyDwell is the rule that replaced action classification: the model can
// reliably answer "is a person present" but not "is this person checking the
// tank" (measured against a full day of real footage - every action-phrased
// prompt either confabulated or missed). So "actioned" means the condition
// held continuously for dwell_seconds, which is a proxy for deliberate work
// rather than someone walking past or a one-frame false positive.
func TestApplyDwell(t *testing.T) {
	base := time.Date(2026, 9, 8, 12, 0, 0, 0, time.Local)
	at := func(s int) time.Time { return base.Add(time.Duration(s) * time.Second) }

	t.Run("no dwell requirement resolves on a single positive", func(t *testing.T) {
		actioned, _, _ := applyDwell(0, true, base, 0)
		if !actioned {
			t.Error("dwell_seconds=0 should resolve on any positive frame")
		}
	})

	t.Run("negative frame never resolves and clears the run", func(t *testing.T) {
		actioned, since, _ := applyDwell(base.Unix(), false, at(600), 120)
		if actioned {
			t.Error("a negative frame must not resolve")
		}
		if since != 0 {
			t.Errorf("a negative frame must clear the run, got since=%d", since)
		}
	})

	t.Run("first positive starts the run but does not resolve", func(t *testing.T) {
		actioned, since, dwelled := applyDwell(0, true, base, 120)
		if actioned {
			t.Error("the first frame of a run has zero dwell and must not resolve")
		}
		if since != base.Unix() {
			t.Errorf("run should start now, got since=%d want %d", since, base.Unix())
		}
		if dwelled != 0 {
			t.Errorf("dwelled should be 0 on the first frame, got %d", dwelled)
		}
	})

	t.Run("resolves only once the threshold is reached", func(t *testing.T) {
		// 60s sampling, 120s requirement: 3rd consecutive detection resolves.
		cases := []struct {
			elapsed int
			want    bool
		}{{0, false}, {60, false}, {119, false}, {120, true}, {180, true}}
		for _, c := range cases {
			actioned, _, dwelled := applyDwell(base.Unix(), true, at(c.elapsed), 120)
			if actioned != c.want {
				t.Errorf("after %ds (dwelled=%d): actioned=%v, want %v", c.elapsed, dwelled, actioned, c.want)
			}
		}
	})

	t.Run("a gap resets accumulated credit", func(t *testing.T) {
		// present at t=0, gone at t=60, back at t=120: the return is the
		// first frame of a NEW run, so it must not inherit the old credit
		// even though 120s have passed since first sighting.
		_, since1, _ := applyDwell(0, true, base, 120)
		_, sinceAfterGap, _ := applyDwell(since1, false, at(60), 120)
		actioned, since2, dwelled := applyDwell(sinceAfterGap, true, at(120), 120)
		if actioned {
			t.Error("credit must not carry across a negative frame")
		}
		if since2 != at(120).Unix() {
			t.Errorf("a new run should start at the return, got since=%d want %d", since2, at(120).Unix())
		}
		if dwelled != 0 {
			t.Errorf("new run should have 0 dwell, got %d", dwelled)
		}
	})
}
