package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// runSelfTest exercises the exact same functions the real service uses
// (loadConfig, grabFrame, prepObjectFrame, runVLM, sendNotification) against
// a real camera pull, real inference, and (unless disabled) a real
// notification send - so a fresh install can be verified end-to-end without
// waiting for the scheduled window to open or a real SOP breach to happen.
// Images are saved under state/selftest/ (not state/crops/, and it never
// touches tank_replenish.json) so they're inspectable afterward but kept
// separate from real detection history.
func runSelfTest(args []string) {
	fs := flag.NewFlagSet("selftest", flag.ExitOnError)
	notify := fs.Bool("notify", true, "send a real test Telegram message (needs notifications.telegram configured)")
	onlyObject := fs.String("object", "", "only test this object id (default: all configured objects)")
	fs.Parse(args)

	fail := func(step string, err error) {
		fmt.Fprintf(os.Stderr, "FAIL  %-8s %v\n", step, err)
		os.Exit(1)
	}
	ok := func(step, detail string) {
		fmt.Printf("OK    %-8s %s\n", step, detail)
	}

	fmt.Println("=== sanddune selftest ===")

	cfg, err := loadConfig()
	if err != nil {
		fail("config", err)
	}
	ok("config", "loaded "+configPath)

	det := cfg.Detectors.TankReplenish
	if !det.Enabled {
		fail("config", fmt.Errorf("detectors.tank_replenish.enabled is false in config.yaml"))
	}
	if det.RTSPURL == "" || strings.Contains(det.RTSPURL, "camera-ip") {
		fail("config", fmt.Errorf("rtsp_url is still the placeholder - edit config.yaml"))
	}

	outDir := filepath.Join(stateDir, "selftest", time.Now().Format("20060102_150405"))
	if err := os.MkdirAll(outDir, 0755); err != nil {
		fail("camera", err)
	}

	framePath := filepath.Join(outDir, "frame.jpg")
	start := time.Now()
	if err := grabFrame(det.RTSPURL, framePath, det.Capture.AspectFixWidthScale); err != nil {
		fail("camera", err)
	}
	ok("camera", fmt.Sprintf("pulled a frame in %.1fs", time.Since(start).Seconds()))

	objects := det.objectsOrDefault()
	if *onlyObject != "" {
		filtered := objects[:0]
		for _, o := range objects {
			if o.ID == *onlyObject {
				filtered = append(filtered, o)
			}
		}
		if len(filtered) == 0 {
			fail("model", fmt.Errorf("no object with id %q in config.yaml", *onlyObject))
		}
		objects = filtered
	}

	for _, obj := range objects {
		objFrame := filepath.Join(outDir, obj.ID+".jpg")
		if err := prepObjectFrame(framePath, obj, det.Capture.MaxEdgePx, det.Capture.Scaler, objFrame); err != nil {
			fail("model", err)
		}
		start = time.Now()
		fields, err := runVLM(cfg, objFrame, det.Action.Prompt)
		if err != nil {
			fail("model", err)
		}
		ok("model", fmt.Sprintf("%s -> %v (%.1fs)", obj.ID, fields, time.Since(start).Seconds()))
	}

	if *notify {
		msg := fmt.Sprintf("[sanddune selftest] Camera + model OK as of %s. This is a test message.", time.Now().Format("15:04:05"))
		sent, err := sendNotification(cfg, msg)
		if err != nil {
			fail("notify", err)
		}
		if sent {
			ok("notify", "real Telegram message sent - check your phone")
		} else {
			ok("notify", "DRY RUN only - notifications.telegram not configured in config.yaml")
		}
	} else {
		fmt.Println("SKIP  notify   (-notify=false)")
	}

	fmt.Println("Images saved to " + outDir)
	fmt.Println("=== all checks passed ===")
}
