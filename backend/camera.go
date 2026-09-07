package main

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"
)

// liveGrabTimeout is the right budget for a live RTSP connection - see
// grabFrame's timeout parameter for why a playback URL needs more.
const liveGrabTimeout = 10 * time.Second

// grabFrame pulls a single current frame from an RTSP stream using ffmpeg
// and writes it as a JPEG to savePath. If aspectFixWidthScale > 1, the frame
// is stretched horizontally by that factor first - see CaptureConfig.
// timeout should be generous for a playback/seek URL (see backfill.go) -
// live connections settle in well under a second, but a DVR seeking into
// recorded footage measurably longer, and a too-tight timeout here reads as
// "camera unreachable" when the real problem is just impatience.
func grabFrame(rtspURL, savePath string, aspectFixWidthScale float64, timeout time.Duration) error {
	if rtspURL == "" || rtspURL == "TBD" {
		return fmt.Errorf("RTSP URL not configured (still 'TBD') - set it before grabbing a frame")
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	args := []string{
		"-y",
		"-rtsp_transport", "tcp",
		"-i", rtspURL,
		"-frames:v", "1",
		"-q:v", "2",
	}
	if aspectFixWidthScale > 1 {
		args = append(args, "-vf", fmt.Sprintf("scale=iw*%g:ih", aspectFixWidthScale))
	}
	args = append(args, savePath)

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg failed to grab frame from %s: %w: %s", rtspURL, err, lastN(stderr.String(), 1500))
	}
	return nil
}
