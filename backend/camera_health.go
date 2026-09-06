package main

import (
	"fmt"
	"time"
)

// cameraHealthState tracks consecutive grabFrame failures across scheduler
// ticks so a DVR/network outage triggers exactly one "camera unreachable"
// notification (not one per failed tick - could be hundreds over a long
// outage) and exactly one "back online" notification on recovery. In-memory
// only: a sanddune restart during an outage starts the miss count over,
// an acceptable tradeoff against persisting cross-restart health state for
// what is fundamentally a live/right-now signal.
type cameraHealthState struct {
	consecutiveFails int
	down             bool
	downSince        time.Time
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
