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
    A --> SMS[Twilio SMS / WhatsApp to admin]
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
      Det -->|deadline breached| Tw[Twilio REST API<br/>direct HTTPS call, no SDK]
      Det -->|deadline breached| Spk["afplay + say (macOS)<br/>local speaker alarm"]
    end
    Tw --> Admin[Admin's phone<br/>SMS / WhatsApp]

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

Everything runs local, including the Twilio call — no backend service. Simpler, no
hosting cost. Tradeoff: if the local machine goes down (crash, power loss), nothing
pages anyone. That's the Watchdog box — future work, not solved today.

## Validation status

**Verified:**
- Model + prompt: 10/10 on a curated test set — positive, negative, and adversarial
  cases (e.g. person pouring water but not into the tank). 200/200 identical outputs
  across repeated runs at temp=0. Compared two model generations and two quantization
  levels before settling on the current config.
- Tank replenishment detector: full state machine (deadline tracking, single-fire
  notification, day/window gating) tested end-to-end via Go integration tests
  (`backend/detector_test.go`) against the real inference pipeline.
- Build: compiles natively on macOS, cross-compiles to Windows.
- Windows hardware test: ran the same integration tests on a real Windows box (Intel
  Core i3-6100, 2 cores/4 threads, 8GB RAM, no GPU). Output was correct — matched
  ground truth exactly — but took ~4.7 minutes per frame, dominated by vision encoding
  (~123s). That's a hardware ceiling on this specific CPU, not a bug: the vision
  encoder is the same size regardless of model/quant choice, so it doesn't shrink with
  a smaller model. Exposed a real bug this same run caught: `runVLM`'s inference
  timeout was hardcoded to 120s, shorter than vision encoding alone took here - now
  configurable (`model.timeout_seconds`, `model.threads`) instead of hardcoded.

**Not yet field-tested:**
- Event-triggered detector (VAT/batch-change): state machine unit-tested against a
  mocked model. Prompts are first drafts — need real reference photos from the target
  cameras before the same validation pass the tank detector went through.
- RTSP capture and Twilio notification: implemented directly against each API, tested
  against bad URLs and missing credentials for graceful failure. Not yet run against a
  live camera or live Twilio account.

## Model approach

Running **InternVL3.5-2B**, quantized to **Q4_K_M** (~1.2GB) via `llama.cpp`, zero-shot
(prompted, not fine-tuned).

- The previous generation (InternVL3, non-3.5) failed the test set at any quantization
  level. InternVL3.5-2B passed cleanly at the same quantization — the gap was the
  training recipe, not precision.
- Prompt wording mattered more than model size: adding a "what is the person doing"
  field before the yes/no answer improved accuracy on ambiguous frames, reproducibly
  across 10 repeated runs both ways.

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
| **Actions** | Twilio SMS/WhatsApp to one number; local speaker alarm (tone + spoken message x3) | USB-triggered physical alarm/beacon; web dashboard for status + config; cloud backend for notification routing; watchdog for machine-down alerts |

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
gitignored — it holds the Twilio auth token.

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

notifications:
  phone_number: "+91XXXXXXXXXX"
  twilio:
    account_sid: ""
    auth_token: ""
    from_number: ""
    channel: sms   # or whatsapp

model:
  path: "gguf/v35/OpenGVLab_InternVL3_5-2B-Q4_K_M.gguf"
  mmproj_path: "gguf/v35/mmproj-OpenGVLab_InternVL3_5-2B-f16.gguf"

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
