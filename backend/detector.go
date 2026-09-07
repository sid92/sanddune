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

	if now.Hour() < deadline && inWindow {
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

			if resolves(det.Action.ResolveWhen, det.Action.ResolveMatch, fields) {
				objectsState[obj.ID] = map[string]any{"actioned": true, "at": now.Format("15:04:05"), "crop": cropRecord}
			} else {
				objectsState[obj.ID] = map[string]any{"actioned": false, "crop": cropRecord}
			}
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

	notified, _ := st["notified"].(bool)
	if pastDeadline && !satisfied && !notified {
		// Two variants: statusMessage's "Time: ...\n..." template reads well
		// as a silent Telegram message, but triggerLocalAlarm passes this
		// straight to `say` - a spoken "Time colon Monday..." would be
		// awkward, so speech gets the plain summary without that line.
		summary := "Tank check incomplete: " + strings.Join(outstanding, ", ")
		if _, err := sendNotification(cfg, statusMessage(summary)); err != nil {
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
