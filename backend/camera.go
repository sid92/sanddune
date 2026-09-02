package main

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"
)

// grabFrame pulls a single current frame from an RTSP stream using ffmpeg
// and writes it as a JPEG to savePath. NOT YET TESTED against a real camera -
// no RTSP source has been available in this environment to validate against.
func grabFrame(rtspURL, savePath string) error {
	if rtspURL == "" || rtspURL == "TBD" {
		return fmt.Errorf("RTSP URL not configured (still 'TBD') - set it before grabbing a frame")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-y",
		"-rtsp_transport", "tcp",
		"-i", rtspURL,
		"-frames:v", "1",
		"-q:v", "2",
		savePath,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg failed to grab frame from %s: %w: %s", rtspURL, err, lastN(stderr.String(), 1500))
	}
	return nil
}
