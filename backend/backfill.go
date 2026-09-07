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

// grabFrameRetrying wraps grabFrame with unbounded retries and escalating
// backoff (3s, 5s, 10s, then capped at 15s) - keep trying until a frame
// actually comes back, rather than giving up on the first failure. Found
// the hard way that a fixed small retry count on a narrow error match
// (originally: only the DVR's "453 Not Enough Bandwidth" response) missed
// almost everything real-world: a ~5min laptop sleep mid-run produced
// hundreds of "Network is unreachable" failures that weren't 453 at all
// and so were never retried, and each one was a real sample permanently
// lost instead of a transient blip worth waiting out. This is a diagnostic
// tool a human runs and can Ctrl-C, not an unattended service, so
// "eventually succeeds or the human notices it's stuck" is an acceptable
// tradeoff against a bounded retry count that risks silently losing
// samples to what's usually a recoverable condition (sleep, wake, a brief
// network drop) rather than a truly permanent one (which would fail
// forever either way, and at least this way it fails LOUDLY and
// repeatedly instead of once and silently).
func grabFrameRetrying(rtspURL, savePath string, aspectFixWidthScale float64, timeout time.Duration, label string, shared *backfillShared) error {
	backoffs := []time.Duration{3 * time.Second, 5 * time.Second, 10 * time.Second, 15 * time.Second}
	for attempt := 1; ; attempt++ {
		err := grabFrame(rtspURL, savePath, aspectFixWidthScale, timeout)
		if err == nil {
			return nil
		}
		backoff := backoffs[len(backoffs)-1]
		if attempt-1 < len(backoffs) {
			backoff = backoffs[attempt-1]
		}
		shared.print("%s  grab attempt %d failed (%v), retrying in %s\n", label, attempt, err, backoff)
		time.Sleep(backoff)
	}
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
		grabFrameRetrying(playbackURL, framePath, det.Capture.AspectFixWidthScale, grabTimeout, t.Format("15:04:05"), shared)

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

// runBackfillTimestamps processes one explicit list of timestamps (not a
// contiguous range - no ramp cadence to compute, since there's no "next
// sample" spacing to adjust when the set of times to check is already
// fixed) - the same per-sample logic as runBackfillChunk otherwise. Used to
// re-check specific gaps from an earlier run (see -timestamps) instead of
// re-scanning a whole window just to fill in what a high failure rate
// skipped the first time.
func runBackfillTimestamps(cfg *Config, det TankDetectorConfig, objects []ObjectConfig, timestamps []time.Time,
	outDir string, grabTimeout time.Duration, shared *backfillShared) {

	for _, t := range timestamps {
		pendingNow := shared.pendingSnapshot(objects)
		if len(pendingNow) == 0 {
			return
		}

		playbackURL := buildPlaybackURL(det.RTSPURL, t)
		framePath := filepath.Join(outDir, "frame_"+t.Format("150405")+".jpg")
		grabFrameRetrying(playbackURL, framePath, det.Capture.AspectFixWidthScale, grabTimeout, t.Format("15:04:05"), shared)

		fullFrame := filepath.Join(outDir, "full_"+t.Format("150405")+".jpg")
		if err := prepObjectFrame(framePath, ObjectConfig{ID: "full"}, det.Capture.MaxEdgePx, det.Capture.Scaler, fullFrame); err != nil {
			shared.print("%s  ramp-check prep failed: %v\n", t.Format("15:04:05"), err)
		} else if rampFields, err := runVLM(cfg, fullFrame, det.Action.Prompt); err != nil {
			shared.print("%s  ramp-check model failed: %v\n", t.Format("15:04:05"), err)
		} else {
			shared.print("%s  overall       %v\n", t.Format("15:04:05"), rampFields)
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
	}
}

// runBackfillFillGaps reads "HH:MM:SS" lines from timestampsPath, splits
// them into contiguous slices across workers concurrent goroutines (an even
// split of the list by index, unrelated to how far apart in time consecutive
// entries happen to be - the list is whatever specific gaps the caller wants
// re-checked, not a range worth chunking by duration), and runs
// runBackfillTimestamps on each slice.
func runBackfillFillGaps(cfg *Config, det TankDetectorConfig, objects []ObjectConfig, timestampsPath string, workers, grabTimeoutSeconds int, keepChecking bool) {
	data, err := os.ReadFile(timestampsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL  -timestamps %q: %v\n", timestampsPath, err)
		os.Exit(1)
	}
	now := time.Now()
	var timestamps []time.Time
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var hour, minute, second int
		if _, err := fmt.Sscanf(line, "%d:%d:%d", &hour, &minute, &second); err != nil {
			fmt.Fprintf(os.Stderr, "FAIL  bad line %q in %s: expected \"HH:MM:SS\": %v\n", line, timestampsPath, err)
			os.Exit(1)
		}
		y, m, d := now.Date()
		timestamps = append(timestamps, time.Date(y, m, d, hour, minute, second, 0, now.Location()))
	}
	if len(timestamps) == 0 {
		fmt.Fprintf(os.Stderr, "FAIL  no timestamps found in %s\n", timestampsPath)
		os.Exit(1)
	}
	if workers < 1 {
		fmt.Fprintf(os.Stderr, "FAIL  -workers must be at least 1, got %d\n", workers)
		os.Exit(1)
	}
	outDir := filepath.Join(stateDir, "backfill", now.Format("20060102_150405"))
	if err := os.MkdirAll(outDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL  could not create %s: %v\n", outDir, err)
		os.Exit(1)
	}

	fmt.Printf("=== sanddune backfill: filling %d gap(s) from %s, %d worker(s) ===\n", len(timestamps), timestampsPath, workers)

	shared := &backfillShared{
		pending: map[string]ObjectConfig{},
		resolved: map[string]struct {
			at     string
			fields map[string]string
		}{},
		keepChecking: keepChecking,
	}
	for _, o := range objects {
		shared.pending[o.ID] = o
	}

	var wg sync.WaitGroup
	chunkSize := (len(timestamps) + workers - 1) / workers
	for w := 0; w < workers; w++ {
		lo := w * chunkSize
		hi := lo + chunkSize
		if lo >= len(timestamps) {
			break
		}
		if hi > len(timestamps) {
			hi = len(timestamps)
		}
		wg.Add(1)
		go func(slice []time.Time) {
			defer wg.Done()
			runBackfillTimestamps(cfg, det, objects, slice, outDir, time.Duration(grabTimeoutSeconds)*time.Second, shared)
		}(timestamps[lo:hi])
	}
	wg.Wait()

	fmt.Println("\n=== summary ===")
	for _, o := range objects {
		if f, ok := shared.resolved[o.ID]; ok {
			fmt.Printf("%-12s RESOLVED at %s\n", o.ID, f.at)
		} else {
			fmt.Printf("%-12s never resolved among the filled-in gaps\n", o.ID)
		}
	}
	fmt.Println("\nImages saved to " + outDir)
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
// own local ramp state. Default is 1 (sequential, the original behavior) -
// raise it deliberately, not as a silent default: a brief burst test showed
// 4 concurrent playback sessions completing cleanly at close to the same
// per-request time as 1 alone, but a real 35-minute run at 4 workers saw a
// 79% grab failure rate, far worse than that quick test predicted and not
// fully explained by the one ~6min sleep interruption that happened during
// it. A short synthetic test does not reliably predict this device's
// behavior under sustained concurrent load - don't trust a worker count
// that hasn't been validated over the actual length of run you intend.
// grabFrameRetrying (unbounded retries, not just on one specific error
// code) is what actually makes any worker count usable in practice, by
// eventually recovering from whatever transient failures concurrency
// produces rather than requiring a lucky failure-free run.
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
	timestampsFile := fs.String("timestamps", "", "path to a file of \"HH:MM:SS\" lines (one per line, today's date) to re-check specifically, instead of scanning a [-start,-end) range - for filling in gaps a previous run's failures left behind, without re-doing everything that already succeeded. Ignores -hours/-start/-end/-fine-interval/-ramp-* when set; -workers still splits the list across concurrent sessions.")
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

	if *timestampsFile != "" {
		runBackfillFillGaps(cfg, det, objects, *timestampsFile, *workers, *grabTimeoutSeconds, *keepChecking)
		return
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
