package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const tankStateName = "tank_replenish"

var tankPromptPath = filepath.Join(promptsDir, "tank_pouring.txt")

// mondayZero converts Go's Weekday (Sunday=0) to the config's day-name
// convention (Monday=0), matching ScheduleConfig.DayIndex().
func mondayZero(t time.Time) int {
	return (int(t.Weekday()) + 6) % 7
}

func runTankCheck(cfg *Config, now time.Time, imageOverride string) (map[string]any, error) {
	sched := cfg.Detectors.TankReplenish.Schedule
	checkDay := sched.DayIndex()
	windowStart := sched.WindowStartHour
	deadline := sched.DeadlineHour

	today := now.Format("2006-01-02")
	st := loadState(tankStateName)

	if st["date"] != today {
		st = map[string]any{"date": today, "replenished": false, "notified": false}
	}

	weekday := mondayZero(now)
	if weekday != checkDay {
		saveState(tankStateName, st)
		return map[string]any{"status": "not_scheduled_day", "state": st}, nil
	}

	inWindow := now.Hour() >= windowStart
	pastDeadline := now.Hour() >= deadline

	replenished, _ := st["replenished"].(bool)
	notified, _ := st["notified"].(bool)

	if !replenished && now.Hour() < deadline && inWindow {
		framePath := imageOverride
		var err error
		if framePath == "" {
			framePath, err = tempJPEGPath()
			if err != nil {
				return nil, err
			}
			if err := grabFrame(cfg.Detectors.TankReplenish.RTSPURL, framePath); err != nil {
				return nil, err
			}
		}

		fields, err := runVLM(cfg, framePath, tankPromptPath)
		if err != nil {
			return nil, err
		}
		if fields["POURING"] == "YES" {
			st["replenished"] = true
			replenished = true
		}
	}

	if pastDeadline && !replenished && !notified {
		message := fmt.Sprintf(
			"Water tank has NOT been replenished as of %02d:00 on %s. Please check.",
			deadline, today,
		)
		if _, err := sendNotification(cfg, "[Tank check] "+message); err != nil {
			return nil, err
		}
		triggerLocalAlarm(cfg, message)
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
