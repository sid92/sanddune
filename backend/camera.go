package main

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"
)

// grabFrame pulls a single current frame from an RTSP stream using ffmpeg
// and writes it as a JPEG to savePath. If aspectFixWidthScale > 1, the frame
// is stretched horizontally by that factor first - see CaptureConfig.
func grabFrame(rtspURL, savePath string, aspectFixWidthScale float64) error {
	if rtspURL == "" || rtspURL == "TBD" {
		return fmt.Errorf("RTSP URL not configured (still 'TBD') - set it before grabbing a frame")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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
