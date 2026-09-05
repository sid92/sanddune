// Headless local SOP alarm service. No UI - everything is driven by
// config.yaml (see config.yaml.example). Samples the configured RTSP
// camera(s) on an interval, runs each frame through a local InternVL model,
// and fires a Telegram notification + local speaker alarm if an expected
// event hasn't happened by its deadline.
package main

import (
	"flag"
	"log"
	"strings"
	"time"
)

func main() {
	// No real flags today, but this makes -h/--help print usage and exit
	// instead of silently falling through into the main loop.
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
