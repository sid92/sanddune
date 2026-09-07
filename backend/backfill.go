package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// isBandwidthLimited reports whether a grabFrame error is the DVR's "453
// Not Enough Bandwidth" RTSP response - measured empirically: this device
// serves 4 concurrent playback sessions cleanly (~3x sequential throughput)
// but starts rejecting some requests with this exact, cleanly-detectable
// error at 6+ concurrent, rather than hanging. Worth a short backoff and
// retry rather than counting it as a real failure.
func isBandwidthLimited(err error) bool {
	return err != nil && strings.Contains(err.Error(), "453")
}

// grabFrameRetrying wraps grabFrame with a few short-backoff retries
// specifically for isBandwidthLimited errors - see its comment. Any other
// error returns immediately, unretried.
func grabFrameRetrying(rtspURL, savePath string, aspectFixWidthScale float64, timeout time.Duration, maxRetries int) error {
	var err error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		err = grabFrame(rtspURL, savePath, aspectFixWidthScale, timeout)
		if err == nil || !isBandwidthLimited(err) {
			return err
		}
		time.Sleep(3 * time.Second)
	}
	return err
}

// backfillShared is the state multiple chunk workers coordinate through:
// which objects are still pending (mutex-protected, since with
// -keep-checking off an object resolved by one worker should stop being
// checked by the others too), when each first resolved, and a print lock so
// concurrent workers' output doesn't interleave mid-line.
type backfillShared struct {
	mu          sync.Mutex
	printMu     sync.Mutex
	pending     map[string]ObjectConfig
	resolved    map[string]struct {
		at     string
		fields map[string]string
	}
	keepChecking bool
}

func (s *backfillShared) print(format string, args ...any) {
	s.printMu.Lock()
	defer s.printMu.Unlock()
	fmt.Printf(format, args...)
}

// pendingSnapshot returns the objects still worth checking right now - all
// of them if keepChecking, otherwise whatever hasn't resolved yet globally.
func (s *backfillShared) pendingSnapshot(all []ObjectConfig) []ObjectConfig {
	if s.keepChecking {
		return all
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []ObjectConfig
	for _, o := range all {
		if _, ok := s.pending[o.ID]; ok {
			out = append(out, o)
		}
	}
	return out
}

func (s *backfillShared) markResolved(id, at string, fields map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, already := s.resolved[id]; !already {
		s.resolved[id] = struct {
			at     string
			fields map[string]string
		}{at, fields}
	}
	if !s.keepChecking {
		delete(s.pending, id)
	}
}

// runBackfillChunk processes one contiguous [start,end) sub-range - the
// exact same per-sample logic runBackfill used to run as a single loop over
// the whole window, now one independent unit of work so runBackfill can run
// several of these concurrently across disjoint time ranges. Ramping state
// (step) is local to this chunk - it wouldn't mean much shared across
// workers covering different, unrelated time periods anyway.
func runBackfillChunk(cfg *Config, det TankDetectorConfig, objects []ObjectConfig, start, end time.Time,
	outDir string, baseInterval, fineInterval time.Duration, rampField, rampEquals string,
	grabTimeout time.Duration, shared *backfillShared) {

	step := baseInterval

	for t := start; t.Before(end); t = t.Add(step) {
		pendingNow := shared.pendingSnapshot(objects)
		if len(pendingNow) == 0 {
			return
		}

		playbackURL := buildPlaybackURL(det.RTSPURL, t)
		framePath := filepath.Join(outDir, "frame_"+t.Format("150405")+".jpg")
		if err := grabFrameRetrying(playbackURL, framePath, det.Capture.AspectFixWidthScale, grabTimeout, 3); err != nil {
			shared.print("%s  grab failed: %v\n", t.Format("15:04:05"), err)
			continue
		}

		// Ramp decision runs once, against the overall (uncropped, but
		// aspect-corrected) frame - not any one object's crop. This is
		// deliberately independent of the per-object loop below, which
		// always runs regardless of what this decides: the ramp signal
		// only controls how soon the NEXT sample happens, never whether
		// this sample's objects get checked.
		activity := false
		fullFrame := filepath.Join(outDir, "full_"+t.Format("150405")+".jpg")
		if err := prepObjectFrame(framePath, ObjectConfig{ID: "full"}, det.Capture.MaxEdgePx, det.Capture.Scaler, fullFrame); err != nil {
			shared.print("%s  ramp-check prep failed: %v\n", t.Format("15:04:05"), err)
		} else if rampFields, err := runVLM(cfg, fullFrame, det.Action.Prompt); err != nil {
			shared.print("%s  ramp-check model failed: %v\n", t.Format("15:04:05"), err)
		} else {
			shared.print("%s  overall       %v\n", t.Format("15:04:05"), rampFields)
			activity = rampFields[rampField] == rampEquals
		}

		for _, obj := range pendingNow {
			id := obj.ID
			objFrame := filepath.Join(outDir, fmt.Sprintf("%s_%s.jpg", id, t.Format("150405")))
			if err := prepObjectFrame(framePath, obj, det.Capture.MaxEdgePx, det.Capture.Scaler, objFrame); err != nil {
				shared.print("%s  %-12s crop failed: %v\n", t.Format("15:04:05"), id, err)
				continue
			}
			fields, err := runVLM(cfg, objFrame, det.Action.Prompt)
			if err != nil {
				shared.print("%s  %-12s model failed: %v\n", t.Format("15:04:05"), id, err)
				continue
			}
			if resolves(det.Action.ResolveWhen, det.Action.ResolveMatch, fields) {
				shared.print("%s  %-12s RESOLVED %v\n", t.Format("15:04:05"), id, fields)
				shared.markResolved(id, t.Format("15:04:05"), fields)
			} else {
				shared.print("%s  %-12s not yet %v\n", t.Format("15:04:05"), id, fields)
			}
		}

		// Ramp: zoom in to fineInterval the moment the ramp field fires, and
		// immediately back off to baseInterval the very next sample it
		// doesn't - no cooldown, the overall-frame check IS the signal.
		if activity {
			if step != fineInterval {
				shared.print("  -> activity detected, sampling every %.0fs from here\n", fineInterval.Seconds())
			}
			step = fineInterval
		} else if step != baseInterval {
			shared.print("  -> no longer present, back to every %.0fs\n", baseInterval.Seconds())
			step = baseInterval
		}
	}
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
//
// Samples at -interval normally, ramping to the tighter -fine-interval
// whenever -ramp-field equals -ramp-equals (default PERSON_PRESENT=YES,
// matching prompts/tank_pouring.txt) - dense detail exactly when something's
// happening, cheap scanning otherwise, since the DVR's ~10s-per-seek
// playback cost makes uniformly-dense sampling over a long window
// impractical (see README "Catching up after downtime"). Deliberately a
// flag, not a hardcoded field name - which field means "look closer" is a
// property of the prompt in use, not something backfill.go should assume.
//
// -workers splits [start,end) into that many contiguous, disjoint
// sub-ranges and runs them concurrently (runBackfillChunk), each with its
// own local ramp state. Measured against the real DVR: 4 concurrent
// playback sessions complete cleanly at roughly the same per-request time
// as 1 alone (~3x net throughput); 6+ starts getting rejected with a clean
// RTSP "453 Not Enough Bandwidth" error rather than hanging, so
// grabFrameRetrying backs off and retries a few times on exactly that
// error. Default is 1 (sequential, the original behavior) - raise it
// deliberately, not as a silent default, since the safe concurrency ceiling
// is specific to what one real device tolerated on one test and could
// differ elsewhere.
func runBackfill(args []string) {
	fs := flag.NewFlagSet("backfill", flag.ExitOnError)
	hours := fs.Float64("hours", 2, "how many hours back to scan, from now - ignored if -start is set")
	startFlag := fs.String("start", "", "start of an explicit window, \"HH:MM\" (today, local time) - e.g. -start=12:00 -end=14:00")
	endFlag := fs.String("end", "", "end of an explicit window, \"HH:MM\" (today, local time); defaults to now if -start is set but this isn't")
	intervalSeconds := fs.Int("interval", 0, "seconds between samples when nothing's happening (default: detectors.tank_replenish.check_interval_seconds)")
	fineIntervalSeconds := fs.Int("fine-interval", 10, "seconds between samples while ramped up - set equal to -interval to disable ramping")
	rampField := fs.String("ramp-field", "PERSON_PRESENT", "parsed field that triggers ramping to -fine-interval when it equals -ramp-equals")
	rampEquals := fs.String("ramp-equals", "YES", "value of -ramp-field that triggers ramping")
	onlyObject := fs.String("object", "", "only check this object id (default: all configured objects)")
	grabTimeoutSeconds := fs.Int("grab-timeout", 30, "seconds to wait per playback grab - a DVR seeking into recorded footage is much slower than a live connection")
	keepChecking := fs.Bool("keep-checking", false, "keep checking every object every sample even after it resolves, instead of stopping (like the live service does) - gives a complete row per timestamp for review, at the cost of extra inference calls")
	workers := fs.Int("workers", 1, "concurrent playback sessions, each covering its own slice of the time window - see the runBackfill doc comment for what's actually been measured safe")
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
	if *workers < 1 {
		fmt.Fprintf(os.Stderr, "FAIL  -workers must be at least 1, got %d\n", *workers)
		os.Exit(1)
	}
	outDir := filepath.Join(stateDir, "backfill", now.Format("20060102_150405"))
	if err := os.MkdirAll(outDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL  could not create %s: %v\n", outDir, err)
		os.Exit(1)
	}

	fineInterval := time.Duration(*fineIntervalSeconds) * time.Second
	baseInterval := time.Duration(interval) * time.Second
	fmt.Printf("=== sanddune backfill: %s to %s, every %ds (ramping to %ds when %s=%s), %d worker(s) ===\n",
		start.Format("15:04:05"), end.Format("15:04:05"), interval, *fineIntervalSeconds, *rampField, *rampEquals, *workers)

	shared := &backfillShared{
		pending: map[string]ObjectConfig{},
		resolved: map[string]struct {
			at     string
			fields map[string]string
		}{},
		keepChecking: *keepChecking,
	}
	for _, o := range objects {
		shared.pending[o.ID] = o
	}

	totalDuration := end.Sub(start)
	chunkDuration := totalDuration / time.Duration(*workers)

	var wg sync.WaitGroup
	for w := 0; w < *workers; w++ {
		chunkStart := start.Add(time.Duration(w) * chunkDuration)
		chunkEnd := chunkStart.Add(chunkDuration)
		if w == *workers-1 {
			chunkEnd = end // last chunk absorbs any rounding remainder
		}
		wg.Add(1)
		go func(chunkStart, chunkEnd time.Time) {
			defer wg.Done()
			runBackfillChunk(cfg, det, objects, chunkStart, chunkEnd, outDir, baseInterval, fineInterval,
				*rampField, *rampEquals, time.Duration(*grabTimeoutSeconds)*time.Second, shared)
		}(chunkStart, chunkEnd)
	}
	wg.Wait()

	fmt.Println("\n=== summary ===")
	for _, o := range objects {
		if f, ok := shared.resolved[o.ID]; ok {
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
