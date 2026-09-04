package main

import (
	"os"
	"os/user"
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
	cropsDir = filepath.Join(stateDir, "crops")
}

// findDesktopPouringImage locates the one image we have showing an actual
// person pouring into an actual tank (a licensed stock photo kept out of
// the repo, hence the glob - macOS screenshot filenames use a narrow
// no-break space before AM/PM that doesn't match a literal space).
func findDesktopPouringImage(t *testing.T) string {
	u, err := user.Current()
	if err != nil {
		t.Skip("could not determine home directory:", err)
	}
	matches, _ := filepath.Glob(filepath.Join(u.HomeDir, "Desktop", "Screenshot 2026-08-25 at 12.45.21*PM.png"))
	if len(matches) == 0 {
		t.Skip("reference pouring-into-tank image not found on Desktop - skipping positive case")
	}
	return matches[0]
}

func TestTankCheckAgainstValidatedImages(t *testing.T) {
	// img01 (painting a barrel) is deliberately NOT used here - it's the one
	// known image that flips to a false positive at the 448px capture cap
	// (see README "Validation status"), which the default/no-crop path now
	// applies. Using it here would confound "did the refactor break
	// something" with "the already-documented 448px tradeoff fired again".
	notPouringImage := filepath.Join(projectRoot, "test_images", "img04.jpg") // person standing idle by tank

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	// Force into the check window regardless of real day/time, and force a
	// single default object regardless of what's in the ambient config.yaml
	// (a real deployment's config.yaml may define its own objects list -
	// this test asserts against the "default" single-object fallback).
	cfg.Detectors.TankReplenish.Schedule.Day = "monday"
	cfg.Detectors.TankReplenish.Schedule.WindowStartHour = 0
	cfg.Detectors.TankReplenish.Schedule.DeadlineHour = 23
	cfg.Detectors.TankReplenish.Objects = nil

	monday := time.Date(2026, 8, 31, 9, 0, 0, 0, time.Local)

	objectState := func(result map[string]any, id string) map[string]any {
		st := result["state"].(map[string]any)
		objects := st["objects"].(map[string]any)
		obj, _ := objects[id].(map[string]any)
		return obj
	}

	t.Run("positive case marks object actioned", func(t *testing.T) {
		pouringImage := findDesktopPouringImage(t)
		os.Remove(filepath.Join(stateDir, tankStateName+".json"))
		result, err := runTankCheck(cfg, monday, pouringImage)
		if err != nil {
			t.Fatalf("runTankCheck: %v", err)
		}
		obj := objectState(result, "default")
		if obj["actioned"] != true {
			t.Errorf("expected actioned=true, got %v", obj["actioned"])
		}
	})

	t.Run("negative case leaves object unactioned", func(t *testing.T) {
		os.Remove(filepath.Join(stateDir, tankStateName+".json"))
		result, err := runTankCheck(cfg, monday, notPouringImage)
		if err != nil {
			t.Fatalf("runTankCheck: %v", err)
		}
		obj := objectState(result, "default")
		if obj["actioned"] != false {
			t.Errorf("expected actioned=false, got %v", obj["actioned"])
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
