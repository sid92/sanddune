// Headless local SOP alarm service. No UI - everything is driven by
// config.yaml (see config.yaml.example). Samples the configured RTSP
// camera(s) on an interval, runs each frame through a local InternVL model,
// and fires a Telegram notification + local speaker alarm if an expected
// event hasn't happened by its deadline.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	// "selftest" is a subcommand, not a flag - dispatch before flag.Parse()
	// touches os.Args, same as go/git/docker-style CLIs.
	if len(os.Args) > 1 && os.Args[1] == "selftest" {
		runSelfTest(os.Args[2:])
		return
	}

	// No real flags on the main path - this exists so -h/--help exits with
	// something useful instead of either falling through into the main loop
	// (no flag.Parse() at all) or printing an empty "Usage of sanddune:"
	// block (flag.Parse() with no flags registered).
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "sanddune takes no flags - it's configured entirely via config.yaml.")
		fmt.Fprintln(os.Stderr, "Edit config.yaml next to this binary, then run: ./sanddune")
		fmt.Fprintln(os.Stderr, "Run './sanddune selftest' to verify camera + model + notifications end-to-end.")
		fmt.Fprintln(os.Stderr, "See README.md for the config reference.")
	}
	flag.Parse()

	log.Printf("Tank detector service starting (project root: %s)", projectRoot)

	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("failed to load config.yaml: %v", err)
	}

	if !cfg.Detectors.TankReplenish.Enabled {
		log.Fatalf("detectors.tank_replenish.enabled is false in config.yaml - nothing to run")
	}

	rtspURL := cfg.Detectors.TankReplenish.RTSPURL
	if rtspURL == "" || strings.Contains(rtspURL, "camera-ip") {
		log.Fatalf("detectors.tank_replenish.rtsp_url in config.yaml is still the placeholder - edit it with your real camera URL before running")
	}

	if cfg.Notifications.Telegram.BotToken == "" || cfg.Notifications.Telegram.ChatID == "" || cfg.Notifications.Telegram.ChatID == "TBD" {
		log.Printf("WARNING: notifications.telegram not configured in config.yaml - alerts will only be logged, not sent. See README 'Configuration'.")
	}

	log.Printf("checking camera connection...")
	testFrame := filepath.Join(os.TempDir(), "sanddune_startup_check.jpg")
	if err := grabFrame(rtspURL, testFrame, cfg.Detectors.TankReplenish.Capture.AspectFixWidthScale); err != nil {
		log.Fatalf("camera check failed: %v\n\nFix detectors.tank_replenish.rtsp_url in config.yaml (wrong IP, credentials, or stream path) and try again.", err)
	}
	os.Remove(testFrame)
	log.Printf("camera check OK")

	runScheduler(cfg.Detectors.TankReplenish.CheckIntervalSeconds)
}

// runScheduler polls the tank check on a fixed interval, all week - the
// check itself is cheap to call outside the scheduled window since
// runTankCheck() only touches the camera/model when actually inside it.
// The loop is strictly sequential (one tick processed at a time; if a check
// runs long, the next tick just waits its turn rather than overlapping).
// Config is re-read from disk each tick so edits to config.yaml take effect
// without a restart.
func runScheduler(intervalSeconds int) {
	ticker := time.NewTicker(time.Duration(intervalSeconds) * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		cfg, err := loadConfig()
		if err != nil {
			log.Printf("scheduled check: failed to reload config.yaml: %v", err)
			continue
		}
		result, err := runTankCheck(cfg, time.Now(), "")
		if err != nil {
			log.Printf("scheduled check error: %v", err)
			continue
		}
		if result["status"] == "checked" {
			log.Printf("scheduled check: %v", result["state"])
		}
	}
}
