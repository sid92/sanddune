// cameracheck is a one-time setup diagnostic: point it at a camera and it
// tells you whether the frame is likely anamorphic (transmitted at the
// wrong proportions, as seen on a Hikvision channel in "1080P Lite" mode -
// half horizontal resolution, no metadata saying so) before you calibrate
// any crop coordinates against it. Run this FIRST for any new camera -
// crop coordinates set against a wrongly-proportioned frame will be wrong.
//
// Usage:
//
//	cameracheck -rtsp "rtsp://user:pass@host:554/path" \
//	  [-isapi-host 1.2.3.4] [-isapi-user admin] [-isapi-pass secret] [-channel 10] \
//	  [-out preview_dir]
package main

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func main() {
	rtspURL := flag.String("rtsp", "", "RTSP URL (required)")
	isapiHost := flag.String("isapi-host", "", "Camera/DVR IP or hostname for ISAPI check (optional, Hikvision-specific)")
	isapiUser := flag.String("isapi-user", "", "ISAPI username")
	isapiPass := flag.String("isapi-pass", "", "ISAPI password")
	channel := flag.Int("channel", 1, "Channel number for the ISAPI video-input check")
	outDir := flag.String("out", ".", "Directory to save preview images into")
	flag.Parse()

	if *rtspURL == "" {
		fmt.Fprintln(os.Stderr, "error: -rtsp is required")
		os.Exit(1)
	}

	fmt.Println("=== Step 1: probing the RTSP stream ===")
	width, height, codec, err := probeStream(*rtspURL)
	if err != nil {
		log.Fatalf("ffprobe failed: %v", err)
	}
	fmt.Printf("codec=%s width=%d height=%d (ratio %.3f)\n", codec, width, height, float64(width)/float64(height))

	suspect := false
	var reasons []string

	ratio := float64(width) / float64(height)
	if ratio < 1.2 {
		suspect = true
		reasons = append(reasons, fmt.Sprintf(
			"width:height ratio is %.3f - unusually portrait-like for what's presumably a landscape-mounted camera "+
				"(normal 16:9 is ~1.78, 4:3 is ~1.33)", ratio))
	}

	if *isapiHost != "" && *isapiUser != "" {
		fmt.Println("\n=== Step 2: checking ISAPI video input config (Hikvision) ===")
		resDesc, err := isapiResDesc(*isapiHost, *isapiUser, *isapiPass, *channel)
		if err != nil {
			fmt.Printf("ISAPI check failed (non-fatal, continuing on stream heuristics alone): %v\n", err)
		} else {
			fmt.Printf("channel %d resDesc = %q\n", *channel, resDesc)
			if strings.Contains(strings.ToLower(resDesc), "lite") {
				suspect = true
				reasons = append(reasons, fmt.Sprintf(
					"ISAPI reports resDesc=%q - Hikvision \"Lite\" modes transmit at half horizontal "+
						"resolution to save bandwidth, meant to be displayed stretched 2x wide", resDesc))
			}
		}
	} else {
		fmt.Println("\n=== Step 2: skipped (no -isapi-host/-isapi-user given) ===")
		fmt.Println("If this is a Hikvision device, pass those flags for a much stronger signal than the ratio heuristic alone.")
	}

	if *isapiHost != "" && *isapiUser != "" {
		fmt.Println("\n=== Step 3: checking the DVR's clock (relevant for backend/backfill.go) ===")
		if err := checkDeviceTime(*isapiHost, *isapiUser, *isapiPass); err != nil {
			fmt.Printf("time check failed (non-fatal): %v\n", err)
		}
	} else {
		fmt.Println("\n=== Step 3: skipped (no -isapi-host/-isapi-user given) ===")
	}

	fmt.Println("\n=== Result ===")
	if !suspect {
		fmt.Println("No anamorphic-stream signals found. Proportions are probably fine - still worth a quick visual")
		fmt.Println("gut-check against the saved preview before calibrating crop coordinates.")
	} else {
		fmt.Println("SUSPECT: this stream is likely anamorphic (wrong proportions as transmitted). Reasons:")
		for _, r := range reasons {
			fmt.Println(" -", r)
		}
		fmt.Println("\nSuggested config.yaml setting: detectors.<name>.capture.aspect_fix_width_scale: 2.0")
	}

	if err := os.MkdirAll(*outDir, 0755); err != nil {
		log.Fatalf("creating output dir: %v", err)
	}
	rawPath := filepath.Join(*outDir, "cameracheck_raw.jpg")
	correctedPath := filepath.Join(*outDir, "cameracheck_corrected_2x.jpg")
	if err := grabFrame(*rtspURL, rawPath, 1.0); err != nil {
		log.Fatalf("saving raw preview: %v", err)
	}
	if err := grabFrame(*rtspURL, correctedPath, 2.0); err != nil {
		log.Fatalf("saving corrected preview: %v", err)
	}
	fmt.Printf("\nSaved for visual comparison:\n  raw:       %s\n  2x-wide:   %s\n", rawPath, correctedPath)
	fmt.Println("Look at both. Whichever one has normally-proportioned objects (round pipes look round,")
	fmt.Println("people look normal width, not stretched or squeezed) tells you the real answer -")
	fmt.Println("the heuristics above are a strong hint, not a substitute for looking.")
}

func probeStream(rtspURL string) (width, height int, codec string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ffprobe",
		"-v", "error",
		"-rtsp_transport", "tcp",
		"-select_streams", "v:0",
		"-show_entries", "stream=width,height,codec_name",
		"-of", "default=noprint_wrappers=1",
		rtspURL,
	)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return 0, 0, "", err
	}

	for _, line := range strings.Split(out.String(), "\n") {
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		switch parts[0] {
		case "width":
			width, _ = strconv.Atoi(parts[1])
		case "height":
			height, _ = strconv.Atoi(parts[1])
		case "codec_name":
			codec = parts[1]
		}
	}
	if width == 0 || height == 0 {
		return 0, 0, "", fmt.Errorf("could not parse width/height from ffprobe output:\n%s", out.String())
	}
	return width, height, codec, nil
}

func grabFrame(rtspURL, savePath string, widthScale float64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	args := []string{"-y", "-rtsp_transport", "tcp", "-i", rtspURL, "-frames:v", "1", "-q:v", "2"}
	if widthScale > 1 {
		args = append(args, "-vf", fmt.Sprintf("scale=iw*%g:ih", widthScale))
	}
	args = append(args, savePath)

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, stderr.String())
	}
	return nil
}

var localTimeRe = regexp.MustCompile(`<localTime>(.*?)</localTime>`)

// checkDeviceTime queries Hikvision ISAPI's /System/time endpoint, which
// reports the device's own clock WITH an explicit UTC offset (e.g.
// "2026-09-07T14:45:03+05:30"), and compares it against this machine's
// clock. backend/backfill.go builds playback URLs by formatting a Go
// time.Time directly (local wall-clock, no timezone conversion) into the
// starttime query param, based on one empirical test against one DVR that
// turned out to just echo those numbers back as its own local time,
// ignoring the "Z" suffix's actual UTC meaning. That assumption silently
// breaks if the DVR's configured timezone differs from this machine's, or
// on different firmware/hardware that actually honors "Z" as real UTC - so
// don't trust it blind, check it here first.
func checkDeviceTime(host, user, pass string) error {
	url := fmt.Sprintf("http://%s/ISAPI/System/time", host)
	body, err := digestGet(url, user, pass)
	if err != nil {
		return err
	}
	m := localTimeRe.FindStringSubmatch(body)
	if m == nil {
		return fmt.Errorf("<localTime> not found in response: %s", body)
	}
	deviceTime, err := time.Parse(time.RFC3339, m[1])
	if err != nil {
		return fmt.Errorf("could not parse device time %q: %w", m[1], err)
	}

	now := time.Now()
	_, deviceOffsetSec := deviceTime.Zone()
	_, localOffsetSec := now.Zone()
	drift := now.Sub(deviceTime)
	if drift < 0 {
		drift = -drift
	}

	fmt.Printf("device reports: %s (UTC offset %+03d:%02d)\n",
		deviceTime.Format("2006-01-02 15:04:05 -07:00"), deviceOffsetSec/3600, (deviceOffsetSec%3600)/60)
	fmt.Printf("this machine:   %s (UTC offset %+03d:%02d)\n",
		now.Format("2006-01-02 15:04:05 -07:00"), localOffsetSec/3600, (localOffsetSec%3600)/60)

	if deviceOffsetSec != localOffsetSec {
		fmt.Printf("MISMATCH: device's UTC offset differs from this machine's by %s.\n",
			(time.Duration(localOffsetSec-deviceOffsetSec) * time.Second).String())
		fmt.Println("backfill.go's playback URLs assume they share a timezone - they don't here.")
		fmt.Println("Fix buildPlaybackURL() to shift by the difference before formatting, or run backfill")
		fmt.Println("from a machine in the DVR's own timezone, and re-verify with a real playback pull")
		fmt.Println("(request a known recent time, check the returned frame's on-screen timestamp).")
	} else if drift > 2*time.Minute {
		fmt.Printf("Offsets match, but clocks disagree by %s (out of sync, not a timezone issue) -\n", drift.Round(time.Second))
		fmt.Println("playback times will be off by roughly that much. Consider fixing the DVR's clock (NTP).")
	} else {
		fmt.Println("OK: same UTC offset, clocks agree - backfill.go's local-time assumption holds for this DVR.")
	}
	return nil
}

var resDescRe = regexp.MustCompile(`<resDesc>(.*?)</resDesc>`)

func isapiResDesc(host, user, pass string, channel int) (string, error) {
	url := fmt.Sprintf("http://%s/ISAPI/System/Video/inputs/channels/%d", host, channel)
	body, err := digestGet(url, user, pass)
	if err != nil {
		return "", err
	}
	m := resDescRe.FindStringSubmatch(body)
	if m == nil {
		return "", fmt.Errorf("resDesc not found in response: %s", body)
	}
	return m[1], nil
}

// digestGet performs an HTTP GET with RFC 7616 Digest authentication - the
// standard library has no built-in support for this (only Basic), and
// Hikvision ISAPI requires Digest, so it's implemented by hand here rather
// than pulling in a dependency for one auth scheme.
func digestGet(url, user, pass string) (string, error) {
	client := &http.Client{Timeout: 10 * time.Second}

	req, _ := http.NewRequest("GET", url, nil)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		b, _ := io.ReadAll(resp.Body)
		return string(b), nil // some devices don't require auth
	}

	challenge := resp.Header.Get("WWW-Authenticate")
	params := parseDigestChallenge(challenge)
	if params["realm"] == "" || params["nonce"] == "" {
		return "", fmt.Errorf("unexpected WWW-Authenticate header: %s", challenge)
	}

	cnonce := randomHex(8)
	nc := "00000001"
	ha1 := md5Hex(user + ":" + params["realm"] + ":" + pass)
	ha2 := md5Hex("GET:" + req.URL.RequestURI())
	response := md5Hex(strings.Join([]string{ha1, params["nonce"], nc, cnonce, "auth", ha2}, ":"))

	authHeader := fmt.Sprintf(
		`Digest username="%s", realm="%s", nonce="%s", uri="%s", qop=auth, nc=%s, cnonce="%s", response="%s", algorithm=MD5`,
		user, params["realm"], params["nonce"], req.URL.RequestURI(), nc, cnonce, response,
	)
	if params["opaque"] != "" {
		authHeader += fmt.Sprintf(`, opaque="%s"`, params["opaque"])
	}

	req2, _ := http.NewRequest("GET", url, nil)
	req2.Header.Set("Authorization", authHeader)
	resp2, err := client.Do(req2)
	if err != nil {
		return "", err
	}
	defer resp2.Body.Close()

	b, err := io.ReadAll(resp2.Body)
	if err != nil {
		return "", err
	}
	if resp2.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d: %s", resp2.StatusCode, string(b))
	}
	return string(b), nil
}

func parseDigestChallenge(header string) map[string]string {
	out := map[string]string{}
	header = strings.TrimPrefix(header, "Digest ")
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		out[kv[0]] = strings.Trim(kv[1], `"`)
	}
	return out
}

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}
