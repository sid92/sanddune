package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// os.Executable()-based path resolution (used by exeDir() for the real
// binary) resolves to the temp go-test build dir, not backend/, when run
// under `go test`. Override the package-level path vars here using the
// test file's own known source location instead, so the test exercises
// real project paths (matching how the compiled binary behaves when run
// from the project root).
func init() {
	_, thisFile, _, _ := runtime.Caller(0)
	backendDir := filepath.Dir(thisFile)
	projectRoot = filepath.Dir(backendDir)
	configPath = filepath.Join(projectRoot, "config.yaml")
	stateDir = filepath.Join(projectRoot, "state")
	promptsDir = filepath.Join(projectRoot, "prompts")
	tankPromptPath = filepath.Join(promptsDir, "tank_pouring.txt")
}

// Exercises the real inference path (llama-mtmd-cli + our validated
// tank_pouring.txt prompt) against known-good test images, bypassing RTSP
// via imageOverride. Requires the model files referenced in config.yaml to
// be present locally - this is an integration test, not a unit test.
func TestTankCheckAgainstValidatedImages(t *testing.T) {
	pouringImage := filepath.Join(projectRoot, "test_images", "img07.jpg")   // person pouring
	notPouringImage := filepath.Join(projectRoot, "test_images", "img04.jpg") // person idle, no pour

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	// Force into the check window regardless of real day/time.
	cfg.Detectors.TankReplenish.Schedule.Day = "monday"
	cfg.Detectors.TankReplenish.Schedule.WindowStartHour = 0
	cfg.Detectors.TankReplenish.Schedule.DeadlineHour = 23

	monday := time.Date(2026, 8, 31, 9, 0, 0, 0, time.Local)

	t.Run("positive case marks replenished", func(t *testing.T) {
		os.Remove(filepath.Join(stateDir, tankStateName+".json"))
		result, err := runTankCheck(cfg, monday, pouringImage)
		if err != nil {
			t.Fatalf("runTankCheck: %v", err)
		}
		st := result["state"].(map[string]any)
		if st["replenished"] != true {
			t.Errorf("expected replenished=true, got %v", st["replenished"])
		}
	})

	t.Run("negative case leaves unreplenished", func(t *testing.T) {
		os.Remove(filepath.Join(stateDir, tankStateName+".json"))
		result, err := runTankCheck(cfg, monday, notPouringImage)
		if err != nil {
			t.Fatalf("runTankCheck: %v", err)
		}
		st := result["state"].(map[string]any)
		if st["replenished"] != false {
			t.Errorf("expected replenished=false, got %v", st["replenished"])
		}
	})

	t.Run("deadline breach marks notified", func(t *testing.T) {
		os.Remove(filepath.Join(stateDir, tankStateName+".json"))
		cfg2 := *cfg
		cfg2.Detectors.TankReplenish.Schedule.DeadlineHour = 0 // already past
		lateSameDay := time.Date(2026, 8, 31, 15, 0, 0, 0, time.Local)
		result, err := runTankCheck(&cfg2, lateSameDay, notPouringImage)
		if err != nil {
			t.Fatalf("runTankCheck: %v", err)
		}
		st := result["state"].(map[string]any)
		if st["notified"] != true {
			t.Errorf("expected notified=true, got %v", st["notified"])
		}
	})

	os.Remove(filepath.Join(stateDir, tankStateName+".json"))
}
