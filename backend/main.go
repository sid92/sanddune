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
		// Not fatal: a camera/DVR outage at the moment of launch shouldn't
		// block the service from starting - checkCameraHealth() (in
		// runScheduler below) will pick this up, alert once the configured
		// miss_threshold is hit, and alert again on recovery. A hard exit
		// here would mean an outage that started before a reboot/restart
		// silently prevents the service (and its own outage alerting) from
		// running at all - worse than starting and reporting real status.
		log.Printf("WARNING: camera check failed at startup (will keep retrying): %v", err)
	} else {
		os.Remove(testFrame)
		log.Printf("camera check OK")
	}

	go runCameraHealthLoop()

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

// runCameraHealthLoop pings the camera on its own fixed cadence, 24x7,
// completely independent of the tank detector's schedule - a DVR/network
// outage doesn't wait for business hours. Runs in its own goroutine
// (started once from main, alongside runScheduler) for the life of the
// process. Config is re-read every cycle, same as runScheduler, so editing
// camera_health.interval_seconds or miss_threshold takes effect without a
// restart.
func runCameraHealthLoop() {
	health := loadCameraHealthState()
	for {
		cfg, err := loadConfig()
		if err != nil {
			log.Printf("camera health: failed to reload config.yaml: %v", err)
			time.Sleep(30 * time.Second)
			continue
		}

		checkCameraHealth(cfg, health)

		time.Sleep(time.Duration(cfg.Detectors.TankReplenish.CameraHealth.IntervalSeconds) * time.Second)
	}
}

// checkCameraHealth pings the camera once - a single lightweight frame
// grab, thrown away immediately - and reports a state transition (down, or
// back up) via Telegram, if any.
func checkCameraHealth(cfg *Config, health *cameraHealthState) {
	det := cfg.Detectors.TankReplenish
	frame, err := tempJPEGPath()
	if err == nil {
		err = grabFrame(det.RTSPURL, frame, det.Capture.AspectFixWidthScale)
		os.Remove(frame)
	}

	logMsg, notifyMsg := health.recordResult(err, det.CameraHealth.MissThreshold)
	if logMsg == "" {
		return
	}
	health.persist()
	log.Printf("camera health: %s", logMsg)
	if _, sendErr := sendNotification(cfg, notifyMsg); sendErr != nil {
		log.Printf("camera health notification failed: %v", sendErr)
	}
}
