# sanddune

Local SOP alarm system. Samples RTSP camera feeds on an interval, runs each frame through
a local vision-language model, and fires an alert if an expected event hasn't happened by
a deadline — e.g. tank not refilled by 2pm, floor not cleaned within 2 hours of a batch
change.

Capture, inference, and notification all run on the local machine. No cloud dependency
in the detection loop.

## System flow

```mermaid
flowchart TD
    T1[Scheduled trigger<br/>e.g. Monday 7am] --> W[Detection window opens]
    T2[Event trigger<br/>e.g. visual cue like a batch change] --> W
    W --> S[Sample a camera frame every N seconds]
    S --> VLM[Local VLM evaluates the frame]
    VLM -->|expected event detected| R[Mark resolved - stop sampling]
    VLM -->|not detected yet| D{Deadline reached?}
    D -->|no| S
    D -->|yes, still not detected| A[Fire alert]
    A --> SMS[Telegram to admin]
    A --> SPK[Local speaker alarm - tone + spoken message, 3x]
```

Two trigger types, same underlying pattern:
- **Scheduled** — fixed day/time window (e.g. Monday 07:00–14:00). `detectors.tank_replenish`
  in `config.yaml`.
- **Event-triggered** — window opens when the VLM detects a visual cue (e.g. a vat rotated
  180° signaling a batch change), runs for a fixed duration after (e.g. 2 hours). Prompts
  not yet validated against real footage — see [Validation status](#validation-status).

## Architecture

```mermaid
flowchart LR
    subgraph cam[Camera]
      Cam[IP Camera] -- RTSP --> FF
    end
    subgraph local["Local machine (Go binary + ffmpeg + llama.cpp, all separate for now)"]
      FF[ffmpeg frame grab] --> VLM["llama-mtmd-cli<br/>InternVL3.5-2B, GGUF, Q4_K_M"]
      VLM --> Det[Detector state machine<br/>schedule / window / notified-once logic]
      Cfg[config.yaml] --> Det
      Det -->|deadline breached| Tw[Telegram Bot API<br/>direct HTTPS call, no SDK]
      Det -->|deadline breached| Spk["afplay + say (macOS)<br/>local speaker alarm"]
    end
    Tw --> Admin[Admin's Telegram chat]

    subgraph future[Future]
      Dash[Web dashboard + login]
      Backend[Cloud backend service]
      USB[USB-triggered physical alarm/beacon]
      Watchdog[Watchdog service - pages admin if the local machine itself goes down]
    end
    Det -.-> Backend
    Backend -.-> Dash
    Det -.-> USB
    Backend -.-> Watchdog
```

Everything runs local, including the Telegram call — no backend service. Simpler, no
hosting cost. Tradeoff: if the local machine goes down (crash, power loss), nothing
pages anyone. That's the Watchdog box — future work, not solved today.

## Validation status

**Verified:**
- Detector state machine: deadline tracking, single-fire notification, day/window
  gating, per-object resolution — tested end-to-end via Go integration tests
  (`backend/detector_test.go`) against the real inference pipeline.
- Build: compiles natively on macOS, cross-compiles to Windows.
- Windows hardware test: ran the same integration tests on a real Windows box (Intel
  Core i3-6100, 2 cores/4 threads, 8GB RAM, no GPU). Output was correct, but took
  ~4.7 min/frame at full resolution, dominated by vision encoding (~123s) - a hardware
  ceiling on this CPU, not a bug (the vision encoder is the same size regardless of
  model/quant choice). Two real fixes came out of that run: `runVLM`'s timeout was
  hardcoded to 120s, shorter than vision encoding alone took there, now configurable
  (`model.timeout_seconds`, `model.threads`); and `-t 4` cut total time ~31% (this chip
  is memory-latency-bound, not core-bound - threading helps more than expected).
- 448px capture cap: ~5.3x faster (image tokens are the actual bottleneck - one full-res
  frame is ~3,400 tokens, ~163s of prefill alone). Tradeoff found in the same test: one
  image (a person painting a barrel, not pouring) flipped to a false positive at 448 -
  the model lost a small object (a paintbrush) it correctly saw at full resolution.
  False positives are the dangerous direction here (only `POURING: YES` resolves an
  object), so 448 is not free precision-wise. Two structurally different prompts failed
  this same image identically, pointing at pixel information lost in the downscale, not
  prompt wording.

- Real camera, real crops: connected to the actual deployment camera (Hikvision DVR,
  channel showing the two-tank room). Found and fixed a real bug there - the channel
  transmits "1080P Lite" mode (half horizontal resolution, no metadata saying so), which
  would have made every crop coordinate wrong. `backend/cmd/cameracheck` now catches this
  automatically for any camera. Both tank crops calibrated against corrected-proportion
  frames and validated 4/4 (each object x a real positive and a real negative) with the
  current prompt - ~1.2-1.3s per crop on Mac at a fixed 4 threads (confirmed from
  llama.cpp's own log, not just trusted from the flag). Notably, `tank_far`'s "positive"
  frame has the operator visible in-shot (shared space between the two tanks) but no
  water going into that specific tank, and the model correctly still said NO - a real
  hard-negative pass, not just an easy case.
- Multi-object resolution (`require: all` across two real objects, not synthetic ones) -
  exercised via the crop test above, both objects tracked independently in state.

**Not yet field-tested:**
- Windows hardware timing on the new crop pipeline - full-frame timing on that box is
  known (~4.7 min/frame, see above); crop+448 should cut that drastically the same way
  it did on Mac, but hasn't been re-measured there yet.
- Telegram notification: implemented directly against the Bot API, not yet sent against
  a real bot/chat.
- `tank_far` has no positive example of its own (a real or synthesized pour into it) -
  only validated as a correct negative so far, including with the operator in-frame.
- Event-triggered detector (VAT/batch-change): state machine unit-tested against a
  mocked model in Python, not ported to the Go service. Prompts are first drafts.
- Our original 8-image stock test set is still weak (only one real hard negative, no
  outdoor-tank scene) - superseded for the tank detector by the real-camera validation
  above, but still what backs the Go integration tests in `backend/detector_test.go`.

## Model approach

Running **InternVL3.5-2B**, quantized to **Q4_K_M** (~1.2GB) via `llama.cpp`, zero-shot
(prompted, not fine-tuned).

- The previous generation (InternVL3, non-3.5) failed the test set at any quantization
  level. InternVL3.5-2B passed cleanly at the same quantization — the gap was the
  training recipe, not precision.
- Earlier prompt iteration found wording mattered more than model size: adding a "what
  is the person doing" field before the yes/no answer improved accuracy on ambiguous
  frames. The current production prompt has since been simplified further to a single
  `POURING: YES/NO` question (person-visibility requirement dropped) - validated 4/4
  against real camera crops, not yet through the same 10-repeated-runs determinism check
  the earlier prompt got — see [Validation status](#validation-status).

**Next**: fine-tune a smaller (~1B) InternVL checkpoint via LoRA once there's a real
bank of labeled footage from the deployed cameras — collect real frames (50-200+ per
class, including known hard cases), fine-tune the HuggingFace checkpoint with InternVL's
`internvl_chat` LoRA scripts, merge, then re-convert to GGUF with the same pipeline
used for the current model. Not started — zero-shot 2B is meeting the bar on available
test data.

## Triggers & actions

| | Available now | Future |
|---|---|---|
| **Triggers** | Scheduled window (day + start hour + deadline hour) | Event-triggered window (visual cue starts an N-hour countdown) - designed, not wired into the Go service |
| **Detection** | Local zero-shot VLM (InternVL3.5-2B) | Fine-tuned local VLM (~1B, LoRA) once labeled data exists |
| **Actions** | Telegram message to one chat; local speaker alarm (tone + spoken message x3) | USB-triggered physical alarm/beacon; web dashboard for status + config; cloud backend for notification routing; watchdog for machine-down alerts |

## Objects and crops

A rule can require the same action on several objects ("both tanks must be refilled").
Each **(object, action) pair is a separate inference call** — never one call asked to
report on every object at once. This keeps prompts single-object, keeps the `KEY: VALUE`
parsing unchanged, and makes object identity come from the loop rather than from model
output.

**Objects are located by static pixel crops.** Two alternatives were considered and
rejected for now:

- *VLM spatial scoping* ("consider only the left tank, ignore the other") — models are
  weak at this, and it puts object identity inside model output where it can't be trusted.
- *A detection/segmentation model* to derive boxes per frame — solves camera drift and
  scales past a handful of objects, but adds a second model and a new failure mode to
  solve a problem not yet demonstrated. Revisit if drift or object count becomes real.

**A crop is an action region, not an object bounding box.** The evidence for "refilling"
(the person, the raised jug, the pour) sits above and around the tank, not inside a tight
box drawn on it. Crops must include headroom for a raised jug and wherever the operator
stands. Padding is anisotropic — mostly upward, modestly sideways.

Two constraints pull against each other: too tight and the action falls outside the crop;
too wide and the crop catches the neighbouring tank, which risks marking an *untouched*
tank as done. That is the dangerous direction, since only a positive resolves a window.

**Before calibrating any crop, check the frame's proportions are actually correct.** A
real Hikvision channel we tested against transmits "1080P Lite" - half horizontal
resolution (960x1080 instead of 1920x1080) with no stream metadata saying so, so it
silently looks squeezed. Crop coordinates set against a wrongly-proportioned frame are
wrong. `backend/cmd/cameracheck` automates this check for any new camera: probes the
RTSP stream's dimensions, optionally queries Hikvision ISAPI for the configured mode,
flags likely anamorphic streams, and saves a raw + corrected preview to eyeball. Run it
first, and if it flags a problem, set `capture.aspect_fix_width_scale` in config.yaml
before doing anything else (applied by `grabFrame` before crop, so crop coordinates
should always be set against the corrected image).

**Crop coordinates are set once, by eye, and stored in config.** There is deliberately no
GUI (see Roadmap). The setup loop is: operator describes the region roughly → render the
crop → operator confirms or corrects → repeat. Two or three rounds converge, and no
interface is needed.

Verify against a frame with a **refill actually in progress**, not an empty scene. An empty
crop only proves the tank is in view; it cannot show whether the operator and jug land
inside the box. If no such frame exists, have someone mime a pour at each tank and grab one.

**Assumption: camera and tanks stay put.** If the camera is nudged, crops keep working but
point at the wrong patch — the detector then reports "not done" forever and alarms every
week with nothing indicating why. Mitigation: persist the crop used for each decision
alongside the parsed fields, so "why didn't it fire" is answered by looking at what the
model actually saw. Those saved crops double as real labelled frames for a representative
eval set and for the LoRA work below.

## Configuration

Copy `config.yaml.example` to `config.yaml` and fill in real values. `config.yaml` is
gitignored — it holds the Telegram bot token.

```yaml
detectors:
  tank_replenish:
    enabled: true
    rtsp_url: "rtsp://user:pass@camera-ip:554/stream"
    schedule:
      day: monday
      window_start_hour: 7
      deadline_hour: 14
    check_interval_seconds: 10

    capture:
      # aspect_fix_width_scale: 2.0  # only for cameras that transmit anamorphic frames
                                      # without saying so - see "Objects and crops" below
      max_edge_px: 448   # fixed - crop first, then cap the long edge, never upscale
      scaler: bicubic

    action:
      prompt: prompts/tank_pouring.txt
      resolve_when:
        - field: POURING
          equals: YES

    objects:              # omit entirely for a single tank (full frame, no crop)
      - id: tank_left
        crop: [0, 0, 0, 0] # [x, y, w, h] - action region, set by eye against a real frame
      - id: tank_right
        crop: [0, 0, 0, 0]

    require: all          # all | any

notifications:
  telegram:
    bot_token: ""   # from @BotFather on Telegram
    chat_id: "TBD"  # message your bot once, then GET the getUpdates endpoint to find it

model:
  path: "gguf/v35/OpenGVLab_InternVL3_5-2B-Q4_K_M.gguf"
  mmproj_path: "gguf/v35/mmproj-OpenGVLab_InternVL3_5-2B-f16.gguf"
  threads: 0           # 0 = auto-detect; set explicitly on weak CPUs
  timeout_seconds: 300  # raise on slow/CPU-only hardware

local_alarm:
  enabled: true
  repeat_count: 3
```

Re-read on every check — edits take effect without a restart.

## Running it

Requires `llama-mtmd-cli` (from `llama.cpp`) and `ffmpeg` on PATH, and the model files
under `gguf/` (see `gguf/v35/`).

```bash
cd backend
go build -o ../sanddune .
cd ..
./sanddune   # finds config.yaml, gguf/, prompts/, state/ next to the binary itself,
             # regardless of what directory it's launched from
```

Cross-compiling for Windows (buildable from macOS, no Windows machine needed for the
build itself — see [Validation status](#validation-status) for what's confirmed on real
Windows hardware):

```bash
cd backend
GOOS=windows GOARCH=amd64 go build -o ../sanddune.exe .
```
Copy `sanddune.exe` alongside `config.yaml`, `gguf/`, `prompts/`, plus Windows builds of
`llama-mtmd-cli.exe` and `ffmpeg.exe` on PATH.

To test detection logic without a live camera, use the Go integration test (bypasses
RTSP via an image file):

```bash
cd backend
go test -v ./...
```

## Roadmap

- **Single-binary packaging**: bundle `ffmpeg` and `llama.cpp` into the Go executable via
  `go:embed` (embed the prebuilt platform binary as data, extract to a temp dir on first
  run) — one file plus a model-weights folder. Weights stay separate either way, same as
  every other local-LLM tool (Ollama, LM Studio) — swappable without a rebuild.
- **Multi-camera fan-out**: one camera per detector today; generalize to N.
- **Service supervision**: watchdog that pages the admin if the local machine goes
  offline — the one failure mode a local-only architecture can't self-report.
- **Web dashboard + cloud backend**: status visibility and config changes without
  touching the file directly.
- **USB-triggered physical alarm/beacon**: local alert channel beyond speakers and phone.
