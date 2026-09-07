package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// buildPlaybackURL turns a live Hikvision RTSP URL into a historical
// playback URL for a specific point in time. Hikvision-specific (ISAPI
// convention: /Streaming/Channels/<N> for live, /Streaming/tracks/<N> for
// playback, same channel number, a starttime query param). The "Z" suffix
// looks like UTC (ISO8601), but this DVR does NOT convert it - it takes the
// numbers as its own local wall-clock time verbatim, confirmed against a
// real recording (requested a known time, checked the returned frame's
// on-screen timestamp matched). An earlier version of this code called
// .UTC() before formatting, silently pulling frames from 5:30 (IST's UTC
// offset) away from the intended time - a real bug that shipped and was
// only caught by eyeballing the saved images.
//
// This assumes the DVR's configured timezone matches this machine's -
// true for this deployment (both IST), verified for real (not just
// inferred) via `cameracheck`'s device-clock check, not just this one
// playback test:
//
//	cameracheck -isapi-host <ip> -isapi-user <user> -isapi-pass <pass> -channel <N> ...
//
// Re-run that check on any new camera/DVR before trusting backfill against
// it - a different device could easily honor "Z" as real UTC, or simply be
// configured to a different timezone than whatever machine runs this.
func buildPlaybackURL(liveURL string, at time.Time) string {
	playbackURL := strings.Replace(liveURL, "/Streaming/Channels/", "/Streaming/tracks/", 1)
	return fmt.Sprintf("%s?starttime=%s", playbackURL, at.Format("20060102T150405Z"))
}

// parseTodayClockTime parses "HH:MM" as today's date (relative to ref) at
// that local clock time.
func parseTodayClockTime(clock string, ref time.Time) (time.Time, error) {
	var hour, minute int
	if _, err := fmt.Sscanf(clock, "%d:%d", &hour, &minute); err != nil {
		return time.Time{}, fmt.Errorf("expected \"HH:MM\", e.g. 12:00: %w", err)
	}
	if hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return time.Time{}, fmt.Errorf("expected \"HH:MM\" with hour 0-23 and minute 0-59, got %q", clock)
	}
	y, m, d := ref.Date()
	return time.Date(y, m, d, hour, minute, 0, 0, ref.Location()), nil
}

// runBackfill re-runs the real detection pipeline (grabFrame against
// historical playback, prepObjectFrame, runVLM, resolves) across a past
// time window instead of the live camera - for catching up after sanddune
// itself was down and missed part of a check window. Report-only: prints
// findings and saves images under state/backfill/<timestamp>/, but does NOT
// touch state/tank_replenish.json - the live service may already be running
// concurrently against that same file, and merging findings automatically
// risks a race with it. Decide by hand whether to edit that file, or just
// let today's live checks continue and possibly still catch it.
func runBackfill(args []string) {
	fs := flag.NewFlagSet("backfill", flag.ExitOnError)
	hours := fs.Float64("hours", 2, "how many hours back to scan, from now - ignored if -start is set")
	startFlag := fs.String("start", "", "start of an explicit window, \"HH:MM\" (today, local time) - e.g. -start=12:00 -end=14:00")
	endFlag := fs.String("end", "", "end of an explicit window, \"HH:MM\" (today, local time); defaults to now if -start is set but this isn't")
	intervalSeconds := fs.Int("interval", 0, "seconds between samples (default: detectors.tank_replenish.check_interval_seconds)")
	onlyObject := fs.String("object", "", "only check this object id (default: all configured objects)")
	grabTimeoutSeconds := fs.Int("grab-timeout", 30, "seconds to wait per playback grab - a DVR seeking into recorded footage is much slower than a live connection")
	fs.Parse(args)

	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL  config  %v\n", err)
		os.Exit(1)
	}
	det := cfg.Detectors.TankReplenish

	interval := *intervalSeconds
	if interval <= 0 {
		interval = det.CheckIntervalSeconds
	}

	objects := det.objectsOrDefault()
	if *onlyObject != "" {
		filtered := objects[:0]
		for _, o := range objects {
			if o.ID == *onlyObject {
				filtered = append(filtered, o)
			}
		}
		if len(filtered) == 0 {
			fmt.Fprintf(os.Stderr, "FAIL  no object with id %q in config.yaml\n", *onlyObject)
			os.Exit(1)
		}
		objects = filtered
	}

	var start, end time.Time
	now := time.Now()
	if *startFlag != "" {
		var err error
		start, err = parseTodayClockTime(*startFlag, now)
		if err != nil {
			fmt.Fprintf(os.Stderr, "FAIL  -start %q: %v\n", *startFlag, err)
			os.Exit(1)
		}
		end = now
		if *endFlag != "" {
			end, err = parseTodayClockTime(*endFlag, now)
			if err != nil {
				fmt.Fprintf(os.Stderr, "FAIL  -end %q: %v\n", *endFlag, err)
				os.Exit(1)
			}
		}
	} else {
		end = now
		start = end.Add(-time.Duration(*hours * float64(time.Hour)))
	}
	if !start.Before(end) {
		fmt.Fprintf(os.Stderr, "FAIL  start (%s) must be before end (%s)\n", start.Format("15:04:05"), end.Format("15:04:05"))
		os.Exit(1)
	}
	outDir := filepath.Join(stateDir, "backfill", now.Format("20060102_150405"))
	if err := os.MkdirAll(outDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL  could not create %s: %v\n", outDir, err)
		os.Exit(1)
	}

	fmt.Printf("=== sanddune backfill: %s to %s, every %ds ===\n",
		start.Format("15:04:05"), end.Format("15:04:05"), interval)

	type finding struct {
		at     string
		fields map[string]string
	}
	resolved := map[string]finding{}
	pending := map[string]ObjectConfig{}
	for _, o := range objects {
		pending[o.ID] = o
	}

	for t := start; t.Before(end) && len(pending) > 0; t = t.Add(time.Duration(interval) * time.Second) {
		playbackURL := buildPlaybackURL(det.RTSPURL, t)
		framePath := filepath.Join(outDir, "frame_"+t.Format("150405")+".jpg")
		if err := grabFrame(playbackURL, framePath, det.Capture.AspectFixWidthScale, time.Duration(*grabTimeoutSeconds)*time.Second); err != nil {
			fmt.Printf("%s  grab failed: %v\n", t.Format("15:04:05"), err)
			continue
		}

		for id, obj := range pending {
			objFrame := filepath.Join(outDir, fmt.Sprintf("%s_%s.jpg", id, t.Format("150405")))
			if err := prepObjectFrame(framePath, obj, det.Capture.MaxEdgePx, det.Capture.Scaler, objFrame); err != nil {
				fmt.Printf("%s  %-12s crop failed: %v\n", t.Format("15:04:05"), id, err)
				continue
			}
			fields, err := runVLM(cfg, objFrame, det.Action.Prompt)
			if err != nil {
				fmt.Printf("%s  %-12s model failed: %v\n", t.Format("15:04:05"), id, err)
				continue
			}
			if resolves(det.Action.ResolveWhen, fields) {
				fmt.Printf("%s  %-12s RESOLVED %v\n", t.Format("15:04:05"), id, fields)
				resolved[id] = finding{at: t.Format("15:04:05"), fields: fields}
				delete(pending, id)
			} else {
				fmt.Printf("%s  %-12s not yet %v\n", t.Format("15:04:05"), id, fields)
			}
		}
	}

	fmt.Println("\n=== summary ===")
	for _, o := range objects {
		if f, ok := resolved[o.ID]; ok {
			fmt.Printf("%-12s RESOLVED at %s during the missed window\n", o.ID, f.at)
		} else {
			fmt.Printf("%-12s never resolved in this window\n", o.ID)
		}
	}
	fmt.Println("\nImages saved to " + outDir)
	fmt.Println("This did NOT change state/tank_replenish.json - the live service (if running) keeps")
	fmt.Println("its own view. Edit that file by hand if you want it to stop expecting an object")
	fmt.Println("that actually resolved during the gap.")
}
