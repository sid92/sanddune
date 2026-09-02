package main

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

var kvLine = regexp.MustCompile(`^([A-Z_]+):\s*(.*?)\s*$`)

// runVLM runs the InternVL GGUF model on one image with one prompt file via
// llama.cpp, returning the KEY: VALUE fields it printed.
func runVLM(cfg *Config, imagePath, promptPath string) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "llama-mtmd-cli",
		"-m", cfg.Model.Path,
		"--mmproj", cfg.Model.MmprojPath,
		"--image", imagePath,
		"-f", promptPath,
		"-n", "150",
		"--temp", "0",
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("llama-mtmd-cli failed: %w: %s", err, lastN(stderr.String(), 2000))
	}

	fields := map[string]string{}
	for _, line := range strings.Split(stdout.String(), "\n") {
		if m := kvLine.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			fields[m[1]] = m[2]
		}
	}
	if len(fields) == 0 {
		return nil, fmt.Errorf("no parseable KEY: VALUE output from model, raw stdout: %s", stdout.String())
	}
	return fields, nil
}

func lastN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
