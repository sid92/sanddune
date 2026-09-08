package main

import (
	"fmt"
	"time"
)

// The two outcome messages Sid specified, kept as pure functions so the exact
// wording that reaches a phone is unit-tested rather than discovered in
// production. Everything variable in them is derived from config - the window
// hours and the dwell requirement - so changing the schedule or the dwell
// threshold cannot leave the text quietly claiming something untrue.

// hourLabel renders a 24h config hour the way the message reads it aloud:
// 9 -> "9am", 14 -> "2pm", 12 -> "12pm", 0 -> "12am".
func hourLabel(h int) string {
	suffix := "am"
	if h >= 12 {
		suffix = "pm"
	}
	display := h % 12
	if display == 0 {
		display = 12
	}
	return fmt.Sprintf("%d%s", display, suffix)
}

// dwellLabel renders the dwell requirement for the compliant message. Whole
// minutes read as "2min"; anything else stays in seconds rather than being
// rounded into a claim the detector didn't actually make.
func dwellLabel(dwellSeconds int) string {
	if dwellSeconds > 0 && dwellSeconds%60 == 0 {
		return fmt.Sprintf("%dmin", dwellSeconds/60)
	}
	return fmt.Sprintf("%ds", dwellSeconds)
}

// compliantMessage is sent the moment the dwell threshold is met, not held
// until the deadline - the good news is actionable immediately, and waiting
// would only delay it by hours. seenAt is when the run of continuous presence
// began, which is the honest answer to "when were they seen", rather than the
// later moment the threshold happened to tip over.
func compliantMessage(seenAt time.Time, dwellSeconds int) string {
	return fmt.Sprintf(
		"✅ Employees were seen at tanks for >%s.\n\nSeen at: %s\n\nDate: %s",
		dwellLabel(dwellSeconds),
		seenAt.Format("03:04 PM"),
		seenAt.Format("02/01 Mon"),
	)
}

// breachMessage is sent once, at the deadline, and only if nothing resolved.
// It states the window it actually watched so the recipient can tell the
// difference between "nobody came" and "the detector was only up for an hour".
func breachMessage(now time.Time, windowStartHour, deadlineHour int) string {
	return fmt.Sprintf(
		"❌ Employees did NOT check or refill tanks. Monitored %s-%s.\n\nDate: %s",
		hourLabel(windowStartHour),
		hourLabel(deadlineHour),
		now.Format("02/01 Mon"),
	)
}

// proofCrop picks the frame sent as evidence: the middle of the run of
// consecutive positive detections. The middle is deliberate - the first frame
// catches someone mid-arrival and the last catches them leaving, while the
// middle is the frame most likely to show the work actually happening.
// Returns "" when there is nothing to send, which callers treat as
// "send the text without a photo" rather than as an error.
func proofCrop(runCrops []string) string {
	if len(runCrops) == 0 {
		return ""
	}
	return runCrops[len(runCrops)/2]
}
