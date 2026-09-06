package main

import (
	"errors"
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
