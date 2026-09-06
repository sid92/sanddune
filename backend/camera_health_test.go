package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCameraHealthStateTransitions(t *testing.T) {
	h := &cameraHealthState{}
	failure := errors.New("connection refused")

	if _, notify := h.recordResult(failure, 3); notify != "" {
		t.Errorf("fail 1 (below threshold): got %q, want empty", notify)
	}
	if _, notify := h.recordResult(failure, 3); notify != "" {
		t.Errorf("fail 2 (below threshold): got %q, want empty", notify)
	}
	logMsg, notify := h.recordResult(failure, 3)
	if !strings.Contains(notify, "DVR offline") {
		t.Errorf("fail 3 (hits threshold): notify message should contain 'DVR offline', got %q", notify)
	}
	if !strings.HasPrefix(notify, "Time: ") {
		t.Errorf("notify message should start with 'Time: ', got %q", notify)
	}
	if strings.Contains(notify, failure.Error()) {
		t.Errorf("notify message should NOT embed the raw error (keep phone alerts short): got %q", notify)
	}
	if !strings.Contains(logMsg, failure.Error()) {
		t.Errorf("log message SHOULD embed the raw error (for debugging): got %q", logMsg)
	}
	if _, notify := h.recordResult(failure, 3); notify != "" {
		t.Errorf("fail 4 (already down): got %q, want empty - no repeat notification while still failing", notify)
	}

	_, notify = h.recordResult(nil, 3)
	if !strings.Contains(notify, "DVR online") {
		t.Errorf("recovery tick: notify message should contain 'DVR online', got %q", notify)
	}
	if _, notify := h.recordResult(nil, 3); notify != "" {
		t.Errorf("second healthy tick: got %q, want empty - no repeat recovery notification", notify)
	}

	// A second outage must notify again - "down" isn't sticky forever.
	if _, notify := h.recordResult(failure, 3); notify != "" {
		t.Errorf("second outage tick 1: got %q, want empty", notify)
	}
	if _, notify := h.recordResult(failure, 3); notify != "" {
		t.Errorf("second outage tick 2: got %q, want empty", notify)
	}
	if _, notify := h.recordResult(failure, 3); notify == "" {
		t.Errorf("second outage tick 3: expected a non-empty unreachable message, got empty")
	}
}

// TestCameraHealthPersistsAcrossRestart is the actual bug this was written
// for: multiple sanddune restarts while the camera stayed down the whole
// time each independently hit their own miss_threshold and re-sent the
// "unreachable" alert, even though nothing had actually changed. Restoring
// down=true from disk must suppress that.
func TestCameraHealthPersistsAcrossRestart(t *testing.T) {
	os.Remove(filepath.Join(stateDir, cameraHealthStateName+".json"))
	defer os.Remove(filepath.Join(stateDir, cameraHealthStateName+".json"))
	failure := errors.New("connection refused")

	h1 := loadCameraHealthState()
	if h1.down {
		t.Fatalf("fresh state (no state file yet) should default to down=false, got true")
	}
	for i := 0; i < 3; i++ {
		h1.recordResult(failure, 3)
	}
	if !h1.down {
		t.Fatalf("expected down=true after 3 failures at threshold 3")
	}
	h1.persist()

	// Simulate a restart: a brand new process loads state fresh from disk.
	h2 := loadCameraHealthState()
	if !h2.down {
		t.Fatalf("restored state should have down=true, got false")
	}
	if _, notify := h2.recordResult(failure, 3); notify != "" {
		t.Errorf("restart while still down must NOT re-alert: got %q, want empty", notify)
	}

	// Recovery after the restart must still fire exactly once.
	if _, notify := h2.recordResult(nil, 3); notify == "" {
		t.Errorf("recovery after restart: expected a non-empty 'reachable again' message, got empty")
	}
}
