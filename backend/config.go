package main

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

func exeDir() string {
	exe, err := os.Executable()
	if err == nil {
		if resolved, err2 := filepath.EvalSymlinks(exe); err2 == nil {
			exe = resolved
		}
		return filepath.Dir(exe)
	}
	wd, _ := os.Getwd()
	return wd
}

var (
	projectRoot = exeDir()
	configPath  = filepath.Join(projectRoot, "config.yaml")
	stateDir    = filepath.Join(projectRoot, "state")
	promptsDir  = filepath.Join(projectRoot, "prompts")
	cropsDir    = filepath.Join(stateDir, "crops")
)

type ScheduleConfig struct {
	Day             string `yaml:"day"` // e.g. "monday"
	WindowStartHour int    `yaml:"window_start_hour"`
	DeadlineHour    int    `yaml:"deadline_hour"`
}

// CaptureConfig controls image prep before inference. AspectFixWidthScale
// corrects cameras that transmit anamorphic frames without SAR/DAR metadata
// to say so (seen on a Hikvision channel in "1080P Lite" mode: transmits
// 960x1080 - half horizontal resolution to save bandwidth - with no stream
// metadata indicating it should be displayed stretched 2x wide). Applied
// BEFORE crop, so crop coordinates are set against correctly-proportioned
// images. 0 or 1 = no correction (the common case). Crop happens next
// (per-object, see ObjectConfig), then MaxEdgePx caps the crop's long edge -
// only ever downscales, never upscales.
type CaptureConfig struct {
	AspectFixWidthScale float64 `yaml:"aspect_fix_width_scale"`
	MaxEdgePx           int     `yaml:"max_edge_px"`
	Scaler              string  `yaml:"scaler"` // ffmpeg scale flag, e.g. "bicubic"
}

// ResolveCondition is one {field, equals} check against the model's parsed
// KEY: VALUE output. An object's action resolves when ALL conditions in
// ActionConfig.ResolveWhen are true - data-driven, so the code never
// references specific field names like PERSON_PRESENT or POURING directly.
type ResolveCondition struct {
	Field  string `yaml:"field"`
	Equals string `yaml:"equals"`
}

type ActionConfig struct {
	Prompt      string             `yaml:"prompt"`
	ResolveWhen []ResolveCondition `yaml:"resolve_when"`
	// ResolveMatch mirrors TankDetectorConfig.Require's all|any vocabulary,
	// but for conditions instead of objects: "all" (default) requires every
	// condition in ResolveWhen to hold, "any" requires just one. Needed once
	// a single prompt reports more than one independently-sufficient signal
	// (e.g. either an operator checked the tank, or water was actually being
	// poured - either one satisfies the SOP, seeing both isn't required).
	ResolveMatch string `yaml:"resolve_match"`
}

// ObjectConfig is one target within a detector (e.g. one tank). Crop is a
// static pixel action-region [x, y, w, h] in the source frame - deliberately
// not a tight bounding box on the object itself, see README "Objects and
// crops". An all-zero crop means "use the full frame" (no crop configured
// yet, or genuinely not needed).
type ObjectConfig struct {
	ID   string `yaml:"id"`
	Crop [4]int `yaml:"crop"`
}

func (o ObjectConfig) hasCrop() bool {
	return o.Crop != [4]int{0, 0, 0, 0}
}

// CameraHealthConfig controls the always-on, 24x7 camera reachability
// check - runs on its own fixed cadence, fully independent of any
// detector's schedule, since a DVR/network outage doesn't respect it.
type CameraHealthConfig struct {
	IntervalSeconds int `yaml:"interval_seconds"` // how often to ping the camera, all day
	MissThreshold   int `yaml:"miss_threshold"`   // consecutive failed pings before alerting
}

type TankDetectorConfig struct {
	Enabled              bool               `yaml:"enabled"`
	RTSPURL              string             `yaml:"rtsp_url"`
	Schedule             ScheduleConfig     `yaml:"schedule"`
	CheckIntervalSeconds int                `yaml:"check_interval_seconds"`
	Capture              CaptureConfig      `yaml:"capture"`
	Action               ActionConfig       `yaml:"action"`
	Objects              []ObjectConfig     `yaml:"objects"`
	Require              string             `yaml:"require"` // "all" or "any"
	CameraHealth         CameraHealthConfig `yaml:"camera_health"`
	// DwellSeconds requires the resolve condition to hold continuously for
	// this long before an object counts as actioned - a single positive
	// frame no longer resolves it. 0 disables the requirement (any single
	// positive resolves, the original behaviour).
	//
	// This exists because the model can reliably answer "is a person here"
	// but NOT "is this person checking the tank" - measured extensively
	// against a full day of real footage, every action-phrased prompt either
	// confabulated or missed. Dwell time is a proxy for the action: someone
	// standing at the tank for two minutes is servicing it; someone caught
	// in a single frame is usually walking past, or a false positive.
	// On a real day's footage (723 frames, 09:00-14:00) presence fired on 35
	// frames forming 14 continuous runs; exactly one reached 120s and resolved.
	// The other 13 were runs of one or two frames - 30s at most - so nothing
	// that looked like actual work was discarded.
	DwellSeconds int `yaml:"dwell_seconds"`
}

// objectsOrDefault returns the configured objects, or a single synthetic
// "default" object (full frame, no crop) if none are configured - keeps a
// simple single-tank config working without requiring an objects list.
func (d TankDetectorConfig) objectsOrDefault() []ObjectConfig {
	if len(d.Objects) > 0 {
		return d.Objects
	}
	return []ObjectConfig{{ID: "default"}}
}

// TelegramConfig addresses one bot and one or more recipients. chat_id (a
// single chat) and chat_ids (a list) both work and are unioned, so an existing
// single-recipient config keeps working untouched while a second person can be
// added without restructuring the file.
type TelegramConfig struct {
	BotToken string   `yaml:"bot_token"`
	ChatID   string   `yaml:"chat_id"`
	ChatIDs  []string `yaml:"chat_ids"`
}

// recipients lists every chat to notify, de-duplicated so a chat named in both
// chat_id and chat_ids doesn't get the same alert twice. "TBD" is the
// placeholder the example config ships with and is never a real chat.
func (t TelegramConfig) recipients() []string {
	var out []string
	seen := map[string]bool{}
	for _, id := range append([]string{t.ChatID}, t.ChatIDs...) {
		if id == "" || id == "TBD" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

type NotificationsConfig struct {
	Telegram TelegramConfig `yaml:"telegram"`
}

type ModelConfig struct {
	Path           string `yaml:"path"`
	MmprojPath     string `yaml:"mmproj_path"`
	Threads        int    `yaml:"threads"`         // 0 = let llama.cpp auto-detect
	TimeoutSeconds int    `yaml:"timeout_seconds"` // per-inference-call timeout
}

type LocalAlarmConfig struct {
	Enabled     bool `yaml:"enabled"`
	RepeatCount int  `yaml:"repeat_count"`
}

type Config struct {
	Detectors struct {
		TankReplenish TankDetectorConfig `yaml:"tank_replenish"`
	} `yaml:"detectors"`
	Notifications NotificationsConfig `yaml:"notifications"`
	Model         ModelConfig         `yaml:"model"`
	LocalAlarm    LocalAlarmConfig    `yaml:"local_alarm"`
}

var dayNameToIndex = map[string]int{
	"monday": 0, "tuesday": 1, "wednesday": 2, "thursday": 3,
	"friday": 4, "saturday": 5, "sunday": 6,
}

func (s ScheduleConfig) DayIndex() int {
	if i, ok := dayNameToIndex[normalizeDay(s.Day)]; ok {
		return i
	}
	return 0
}

func normalizeDay(d string) string {
	out := make([]byte, 0, len(d))
	for i := 0; i < len(d); i++ {
		c := d[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out = append(out, c)
	}
	return string(out)
}

// loadConfig re-reads config.yaml fresh each call so the running service
// always reflects the latest saved file without needing a restart.
func loadConfig() (*Config, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", configPath, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", configPath, err)
	}

	// sane defaults
	if cfg.Detectors.TankReplenish.CheckIntervalSeconds == 0 {
		cfg.Detectors.TankReplenish.CheckIntervalSeconds = 60
	}
	if cfg.Detectors.TankReplenish.Schedule.Day == "" {
		cfg.Detectors.TankReplenish.Schedule.Day = "monday"
	}
	if cfg.Detectors.TankReplenish.Action.Prompt == "" {
		cfg.Detectors.TankReplenish.Action.Prompt = filepath.Join(promptsDir, "tank_pouring.txt")
	}
	if len(cfg.Detectors.TankReplenish.Action.ResolveWhen) == 0 {
		cfg.Detectors.TankReplenish.Action.ResolveWhen = []ResolveCondition{{Field: "POURING", Equals: "YES"}}
	}
	if cfg.Detectors.TankReplenish.Action.ResolveMatch == "" {
		cfg.Detectors.TankReplenish.Action.ResolveMatch = "all"
	}
	if cfg.Detectors.TankReplenish.Require == "" {
		cfg.Detectors.TankReplenish.Require = "all"
	}
	if cfg.Detectors.TankReplenish.Capture.MaxEdgePx == 0 {
		cfg.Detectors.TankReplenish.Capture.MaxEdgePx = 448
	}
	if cfg.Detectors.TankReplenish.CameraHealth.MissThreshold == 0 {
		cfg.Detectors.TankReplenish.CameraHealth.MissThreshold = 2
	}
	if cfg.Detectors.TankReplenish.CameraHealth.IntervalSeconds == 0 {
		// 15 minutes, not seconds: an outage is measured in hours and someone
		// has to physically go and look at the DVR, so learning about it a few
		// minutes sooner changes nothing while polling every 30s hammers the
		// DVR ~2,900 times a day for that non-difference. Combined with the
		// default miss_threshold of 2, worst-case time to alert is 30 minutes.
		cfg.Detectors.TankReplenish.CameraHealth.IntervalSeconds = 900
	}
	if cfg.Model.Path == "" {
		cfg.Model.Path = "gguf/v35/OpenGVLab_InternVL3_5-2B-Q4_K_M.gguf"
	}
	if cfg.Model.MmprojPath == "" {
		cfg.Model.MmprojPath = "gguf/v35/mmproj-OpenGVLab_InternVL3_5-2B-f16.gguf"
	}
	if cfg.LocalAlarm.RepeatCount == 0 {
		cfg.LocalAlarm.RepeatCount = 3
	}
	if cfg.Model.TimeoutSeconds == 0 {
		// 120s worked fine on the Mac dev machine but blew past on a slow
		// CPU-only Windows box (vision encoding alone took ~123s there) -
		// default generous, let slow/fast hardware both tune it explicitly.
		cfg.Model.TimeoutSeconds = 300
	}

	// Resolve relative paths against the project root, not whatever the
	// process's current working directory happens to be.
	if !filepath.IsAbs(cfg.Model.Path) {
		cfg.Model.Path = filepath.Join(projectRoot, cfg.Model.Path)
	}
	if !filepath.IsAbs(cfg.Model.MmprojPath) {
		cfg.Model.MmprojPath = filepath.Join(projectRoot, cfg.Model.MmprojPath)
	}
	if !filepath.IsAbs(cfg.Detectors.TankReplenish.Action.Prompt) {
		cfg.Detectors.TankReplenish.Action.Prompt = filepath.Join(projectRoot, cfg.Detectors.TankReplenish.Action.Prompt)
	}

	return &cfg, nil
}
