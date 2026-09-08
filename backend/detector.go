package main

import (
	"os"
	"strings"
	"time"
)

const tankStateName = "tank_replenish"

// mondayZero converts Go's Weekday (Sunday=0) to the config's day-name
// convention (Monday=0), matching ScheduleConfig.DayIndex().
func mondayZero(t time.Time) int {
	return (int(t.Weekday()) + 6) % 7
}

// resolves reports whether resolveWhen holds against the model's parsed
// KEY: VALUE fields - AND across the list if match is "any" other than
// "any" (including "", "all", or anything else - "all" is the sane
// default), OR across the list if match is "any". Data-driven and
// intentionally ignorant of specific field names - the prompt defines the
// contract, not the code.
func resolves(resolveWhen []ResolveCondition, match string, fields map[string]string) bool {
	if len(resolveWhen) == 0 {
		return false
	}
	if match == "any" {
		for _, cond := range resolveWhen {
			if fields[cond.Field] == cond.Equals {
				return true
			}
		}
		return false
	}
	for _, cond := range resolveWhen {
		if fields[cond.Field] != cond.Equals {
			return false
		}
	}
	return true
}

// applyDwell tracks how long the resolve condition has held continuously.
// prevSince is when the current unbroken run of positive frames started (0
// if there is no run in progress). A negative frame clears the run, so
// credit never accumulates across a gap. Returns whether the object is now
// actioned, the run's start time to persist, and how long it has run.
//
// dwellSeconds <= 0 means no dwell requirement: any single positive frame
// resolves, which is the behaviour this predates.
func applyDwell(prevSince int64, hit bool, now time.Time, dwellSeconds int) (actioned bool, since, dwelled int64) {
	if !hit {
		return false, 0, 0
	}
	if dwellSeconds <= 0 {
		return true, 0, 0
	}
	since = prevSince
	if since <= 0 {
		since = now.Unix() // first frame of a new run
	}
	dwelled = now.Unix() - since
	return dwelled >= int64(dwellSeconds), since, dwelled
}

// stringList reads a []string back out of persisted state. JSON round-trips
// it as []any, and a fresh day has no key at all, so both shapes decode to an
// empty list rather than panicking.
func stringList(v any) []string {
	items, ok := v.([]any)
	if !ok {
		if already, ok := v.([]string); ok {
			return already
		}
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// asInt64 reads a number back out of detector state, which holds two shapes
// for the same field depending on when it is read. Within the cycle that
// wrote it the value is still a native int64; once it has been saved and
// reloaded, JSON has turned it into a float64. Asserting only one of those
// silently yields the zero value on the other - which is exactly the cycle
// that sends the compliant message, so it would have reported the resolve
// time instead of when people first appeared.
func asInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case float64:
		return int64(n), true
	case int:
		return int64(n), true
	}
	return 0, false
}

// allActioned reports whether every object has already resolved today, in
// which case there is nothing left to look for and the cycle can skip the
// camera pull and inference entirely.
func allActioned(objects []ObjectConfig, objectsState map[string]any) bool {
	for _, obj := range objects {
		objState, _ := objectsState[obj.ID].(map[string]any)
		if actioned, _ := objState["actioned"].(bool); !actioned {
			return false
		}
	}
	return true
}

// resolvedEvidence answers the two questions the compliant message asks: when
// were they seen, and which frame proves it. "When" is the earliest run start
// among resolved objects - the moment people first appeared, not the later
// moment the dwell threshold tipped over. Falls back to now if state somehow
// carries no start time, so the message is never blank.
func resolvedEvidence(objects []ObjectConfig, objectsState map[string]any, now time.Time) (time.Time, string) {
	seenAt, proof := int64(0), ""
	for _, obj := range objects {
		objState, _ := objectsState[obj.ID].(map[string]any)
		if actioned, _ := objState["actioned"].(bool); !actioned {
			continue
		}
		if since, ok := asInt64(objState["since_unix"]); ok && since > 0 {
			if seenAt == 0 || since < seenAt {
				seenAt = since
			}
		}
		if proof == "" {
			proof = proofCrop(stringList(objState["run_crops"]))
		}
	}
	if seenAt == 0 {
		return now, proof
	}
	return time.Unix(seenAt, 0), proof
}

func runTankCheck(cfg *Config, now time.Time, imageOverride string) (map[string]any, error) {
	det := cfg.Detectors.TankReplenish
	checkDay := det.Schedule.DayIndex()
	windowStart := det.Schedule.WindowStartHour
	deadline := det.Schedule.DeadlineHour

	today := now.Format("2006-01-02")
	st := loadState(tankStateName)
	if st["date"] != today {
		st = map[string]any{"date": today, "objects": map[string]any{}, "notified": false}
	}
	objectsState, _ := st["objects"].(map[string]any)
	if objectsState == nil {
		objectsState = map[string]any{}
	}

	weekday := mondayZero(now)
	if weekday != checkDay {
		saveState(tankStateName, st)
		return map[string]any{"status": "not_scheduled_day", "state": st}, nil
	}

	inWindow := now.Hour() >= windowStart
	pastDeadline := now.Hour() >= deadline
	objects := det.objectsOrDefault()

	if now.Hour() < deadline && inWindow && !allActioned(objects, objectsState) {
		frame := imageOverride
		if frame == "" {
			var err error
			frame, err = tempJPEGPath()
			if err != nil {
				return nil, err
			}
			if err := grabFrame(det.RTSPURL, frame, det.Capture.AspectFixWidthScale, liveGrabTimeout); err != nil {
				return nil, err
			}
		}

		for _, obj := range objects {
			objState, _ := objectsState[obj.ID].(map[string]any)
			if actioned, _ := objState["actioned"].(bool); actioned {
				continue // already resolved today - don't pay for it again
			}

			prepped, err := tempJPEGPath()
			if err != nil {
				return nil, err
			}
			if err := prepObjectFrame(frame, obj, det.Capture.MaxEdgePx, det.Capture.Scaler, prepped); err != nil {
				return nil, err
			}

			fields, err := runVLM(cfg, prepped, det.Action.Prompt)
			if err != nil {
				return nil, err
			}

			cropRecord, _ := saveDecisionCrop(tankStateName, obj.ID, now, prepped)
			hit := resolves(det.Action.ResolveWhen, det.Action.ResolveMatch, fields)

			prevSince, _ := asInt64(objState["since_unix"])
			actioned, since, dwelled := applyDwell(prevSince, hit, now, det.DwellSeconds)
			entry := map[string]any{"actioned": actioned, "crop": cropRecord}
			if since > 0 {
				entry["since_unix"] = since
				entry["dwelled_s"] = dwelled
			}
			if actioned {
				entry["at"] = now.Format("15:04:05")
			}
			// Keep every crop of the current run so the proof photo can be
			// the middle frame rather than whichever one happened to tip the
			// threshold. A negative frame clears the run, so this list always
			// describes one unbroken stretch of presence.
			if hit {
				entry["run_crops"] = append(stringList(objState["run_crops"]), cropRecord)
			}
			objectsState[obj.ID] = entry
		}
	}

	var outstanding []string
	for _, obj := range objects {
		objState, _ := objectsState[obj.ID].(map[string]any)
		if actioned, _ := objState["actioned"].(bool); !actioned {
			outstanding = append(outstanding, obj.ID)
		}
	}
	satisfied := len(outstanding) == 0
	if det.Require == "any" {
		satisfied = len(outstanding) < len(objects)
	}

	st["objects"] = objectsState

	// Two independent, once-per-day notifications with opposite timing. The
	// compliant one goes out the moment the dwell threshold is met, because
	// waiting until the deadline would sit on good news for hours. The breach
	// one can only be sent at the deadline, since until then the day can
	// still be saved by someone turning up.
	successNotified, _ := st["success_notified"].(bool)
	if satisfied && !successNotified {
		seenAt, proof := resolvedEvidence(objects, objectsState, now)
		if _, err := sendNotificationPhoto(cfg, compliantMessage(seenAt, det.DwellSeconds), proof); err != nil {
			return nil, err
		}
		st["success_notified"] = true
	}

	notified, _ := st["notified"].(bool)
	if pastDeadline && !satisfied && !notified {
		// The spoken alarm gets a plain summary rather than the Telegram text:
		// `say` would read the emoji and the "Date:" line out loud, which is
		// useless as an audible alert.
		summary := "Tank check incomplete: " + strings.Join(outstanding, ", ")
		if _, err := sendNotification(cfg, breachMessage(now, windowStart, deadline)); err != nil {
			return nil, err
		}
		triggerLocalAlarm(cfg, summary)
		st["notified"] = true
	}

	if err := saveState(tankStateName, st); err != nil {
		return nil, err
	}
	return map[string]any{"status": "checked", "state": st}, nil
}

func tempJPEGPath() (string, error) {
	f, err := os.CreateTemp("", "frame-*.jpg")
	if err != nil {
		return "", err
	}
	path := f.Name()
	f.Close()
	return path, nil
}
