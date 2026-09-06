package main

import (
	"fmt"
	"time"
)

const cameraHealthStateName = "camera_health"

// cameraHealthState tracks consecutive grabFrame failures across checks so
// a DVR/network outage triggers exactly one "camera unreachable"
// notification (not one per failed check - could be hundreds over a long
// outage) and exactly one "back online" notification on recovery. down and
// downSince are persisted to state/camera_health.json (see loadCameraHealthState
// / persist below) specifically so a sanddune restart - or a crash, or a
// deliberate update - while the camera is already known to be down does NOT
// re-fire the "unreachable" alert; only an actual up-to-down transition
// should ever notify, restarts shouldn't matter to that.
type cameraHealthState struct {
	consecutiveFails int
	down             bool
	downSince        time.Time
}

// loadCameraHealthState restores down/downSince from disk (defaulting to
// "up, never been down" if there's no state file yet - a fresh install, or
// state/ was cleared).
func loadCameraHealthState() *cameraHealthState {
	st := loadState(cameraHealthStateName)
	h := &cameraHealthState{}
	if down, ok := st["down"].(bool); ok {
		h.down = down
	}
	if s, ok := st["down_since"].(string); ok {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			h.downSince = t
		}
	}
	return h
}

// persist saves down/downSince - call this only when recordResult reports a
// transition (a non-empty result), not every check; the down/up state is
// the only part worth surviving a restart.
func (h *cameraHealthState) persist() {
	saveState(cameraHealthStateName, map[string]any{
		"down":       h.down,
		"down_since": h.downSince.Format(time.RFC3339),
	})
}

// recordResult updates health state from one camera-reachability attempt.
// logMsg (full detail, including the raw error - which for ffmpeg is often
// its whole version/build banner, fine for a log file) and notifyMsg (a
// short human sentence, safe to push to a phone) are both empty unless this
// call crosses a state transition (down, or back up).
func (h *cameraHealthState) recordResult(err error, missThreshold int) (logMsg, notifyMsg string) {
	if err == nil {
		wasDown := h.down
		h.consecutiveFails = 0
		h.down = false
		if wasDown {
			return fmt.Sprintf("camera reachable again after %s", time.Since(h.downSince).Round(time.Second)),
				statusMessage("DVR online")
		}
		return "", ""
	}

	h.consecutiveFails++
	if h.down {
		return "", "" // already alerted for this outage - don't repeat every tick
	}
	if h.consecutiveFails < missThreshold {
		return "", ""
	}
	h.down = true
	h.downSince = time.Now()
	return fmt.Sprintf("camera unreachable after %d consecutive failed attempts: %v", h.consecutiveFails, err),
		statusMessage("DVR offline")
}

// statusMessage formats the exact two-line, phone-friendly text Sid asked
// for - kept separate from the detailed logMsg above, which carries the raw
// ffmpeg error for local debugging instead.
func statusMessage(status string) string {
	return fmt.Sprintf("Time: %s\n%s", time.Now().Format("Mon 01/02 03:04 PM"), status)
}
