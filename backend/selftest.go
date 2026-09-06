package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// checkResult is one line of the selftest report. status is "OK", "FAIL",
// or "SKIP" (SKIP means a prerequisite check already failed, not that this
// check itself was tried and failed).
type checkResult struct {
	status, name, detail string
}

// runSelfTest exercises the exact same functions the real service uses
// (loadConfig, grabFrame, prepObjectFrame, runVLM, sendNotification) against
// a real camera pull, real inference, and (unless disabled) a real
// notification send - so a fresh install can be verified end-to-end without
// waiting for the scheduled window to open or a real SOP breach to happen.
//
// Every check runs regardless of whether an earlier one failed, and the
// full report prints at the end - a broken camera shouldn't hide whether
// Telegram is configured correctly, since those are independent things to
// fix. The one exception is config.yaml itself failing to parse: nothing
// else can be checked without it.
//
// Images are saved under state/selftest/ (not state/crops/, and it never
// touches tank_replenish.json) so they're inspectable afterward but kept
// separate from real detection history.
func runSelfTest(args []string) {
	fs := flag.NewFlagSet("selftest", flag.ExitOnError)
	notify := fs.Bool("notify", true, "send a real test Telegram message (needs notifications.telegram configured)")
	onlyObject := fs.String("object", "", "only test this object id (default: all configured objects)")
	fs.Parse(args)

	fmt.Println("=== sanddune selftest ===")

	var results []checkResult
	report := func(status, name, detail string) {
		results = append(results, checkResult{status, name, detail})
		fmt.Printf("%-4s  %-14s %s\n", status, name, detail)
	}

	cfg, err := loadConfig()
	if err != nil {
		report("FAIL", "config", err.Error())
		printSelfTestSummary(results) // always exits here - nothing else is checkable without cfg
		return
	}
	report("OK", "config", "loaded "+configPath)

	det := cfg.Detectors.TankReplenish
	if det.Enabled {
		report("OK", "config", "detectors.tank_replenish.enabled")
	} else {
		report("FAIL", "config", "detectors.tank_replenish.enabled is false in config.yaml")
	}

	placeholderRTSP := det.RTSPURL == "" || strings.Contains(det.RTSPURL, "camera-ip")
	if placeholderRTSP {
		report("FAIL", "config", "rtsp_url is still the placeholder - edit config.yaml")
	} else {
		report("OK", "config", "rtsp_url is set")
	}

	var outDir, framePath string
	cameraOK := false
	if placeholderRTSP {
		report("SKIP", "camera", "rtsp_url not configured, nothing to pull from")
	} else {
		outDir = filepath.Join(stateDir, "selftest", time.Now().Format("20060102_150405"))
		if err := os.MkdirAll(outDir, 0755); err != nil {
			report("FAIL", "camera", "could not create "+outDir+": "+err.Error())
		} else {
			framePath = filepath.Join(outDir, "frame.jpg")
			start := time.Now()
			if err := grabFrame(det.RTSPURL, framePath, det.Capture.AspectFixWidthScale); err != nil {
				report("FAIL", "camera", err.Error())
			} else {
				cameraOK = true
				report("OK", "camera", fmt.Sprintf("pulled a frame in %.1fs", time.Since(start).Seconds()))
			}
		}
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
			report("FAIL", "model", fmt.Sprintf("no object with id %q in config.yaml", *onlyObject))
		}
		objects = filtered
	}

	if !cameraOK {
		for _, obj := range objects {
			report("SKIP", "model:"+obj.ID, "no frame available (camera check failed above)")
		}
	} else {
		for _, obj := range objects {
			objFrame := filepath.Join(outDir, obj.ID+".jpg")
			if err := prepObjectFrame(framePath, obj, det.Capture.MaxEdgePx, det.Capture.Scaler, objFrame); err != nil {
				report("FAIL", "model:"+obj.ID, err.Error())
				continue
			}
			start := time.Now()
			fields, err := runVLM(cfg, objFrame, det.Action.Prompt)
			if err != nil {
				report("FAIL", "model:"+obj.ID, err.Error())
				continue
			}
			report("OK", "model:"+obj.ID, fmt.Sprintf("%v (%.1fs)", fields, time.Since(start).Seconds()))
		}
	}

	if !*notify {
		report("SKIP", "notify", "-notify=false")
	} else {
		msg := fmt.Sprintf("[sanddune selftest] Test message as of %s.", time.Now().Format("15:04:05"))
		sent, err := sendNotification(cfg, msg)
		switch {
		case err != nil:
			report("FAIL", "notify", err.Error())
		case sent:
			report("OK", "notify", "real Telegram message sent - check your phone")
		default:
			report("OK", "notify", "DRY RUN only - notifications.telegram not configured in config.yaml")
		}
	}

	if cameraOK {
		fmt.Println("Images saved to " + outDir)
	} else if outDir != "" {
		os.Remove(outDir) // nothing was ever written into it - no-ops if somehow non-empty
	}
	printSelfTestSummary(results)
}

// printSelfTestSummary tallies results and exits 1 if anything FAILed - the
// report above always runs in full regardless, this only decides the
// process exit code for scripting.
func printSelfTestSummary(results []checkResult) {
	failed := 0
	for _, r := range results {
		if r.status == "FAIL" {
			failed++
		}
	}
	fmt.Println("=== summary ===")
	if failed == 0 {
		fmt.Println("All checks passed.")
		return
	}
	fmt.Printf("%d check(s) failed - see FAIL lines above.\n", failed)
	os.Exit(1)
}
