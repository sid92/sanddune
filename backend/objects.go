package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// prepObjectFrame crops the source frame to the object's action region (if
// configured) and caps the long edge at maxEdgePx - never upscales, since
// min(iw,N)/min(ih,N) with force_original_aspect_ratio=decrease can only
// shrink. Crop happens before scaling so the pixel budget is spent on the
// object, not the surrounding scene.
func prepObjectFrame(sourcePath string, obj ObjectConfig, maxEdgePx int, scaler string, outPath string) error {
	if scaler == "" {
		scaler = "bicubic"
	}

	var filter string
	if obj.hasCrop() {
		x, y, w, h := obj.Crop[0], obj.Crop[1], obj.Crop[2], obj.Crop[3]
		filter = fmt.Sprintf("crop=%d:%d:%d:%d,", w, h, x, y)
	}
	if maxEdgePx > 0 {
		filter += fmt.Sprintf(
			"scale='min(iw,%d)':'min(ih,%d)':force_original_aspect_ratio=decrease:flags=%s",
			maxEdgePx, maxEdgePx, scaler,
		)
	} else {
		filter = trimTrailingComma(filter)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	args := []string{"-y", "-i", sourcePath}
	if filter != "" {
		args = append(args, "-vf", filter)
	}
	args = append(args, outPath)

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg crop/scale failed for object %q: %w: %s", obj.ID, err, lastN(stderr.String(), 1000))
	}
	return nil
}

func trimTrailingComma(s string) string {
	if len(s) > 0 && s[len(s)-1] == ',' {
		return s[:len(s)-1]
	}
	return s
}

// saveDecisionCrop persists the exact image the model saw for one
// (detector, object, check) so drift is debuggable and the frames
// accumulate as real eval/training data - see README "Objects and crops".
func saveDecisionCrop(detectorName, objectID string, now time.Time, imagePath string) (string, error) {
	dir := filepath.Join(cropsDir, detectorName, now.Format("2006-01-02"))
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	dest := filepath.Join(dir, fmt.Sprintf("%s_%s.jpg", objectID, now.Format("150405")))

	data, err := os.ReadFile(imagePath)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(dest, data, 0644); err != nil {
		return "", err
	}
	return dest, nil
}
