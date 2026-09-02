# sanddune

Local SOP Alarm System

A local, camera-based compliance watchdog. It samples RTSP camera feeds on an interval,
runs each frame through a local vision-language model, and fires an alert if an expected
event *hasn't* happened by a deadline — e.g. "the tank wasn't refilled by 2pm" or "the
floor wasn't cleaned within 2 hours of a batch change."

No cloud dependency for the detection loop itself: capture, inference, and notification
all happen on the local machine. Built fast and validated in stages — see
[Validation status](#validation-status) for exactly what's been tested and how.

## System flow

Every detector follows the same shape, regardless of what triggers it or what it's
looking for:

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

Two trigger flavors map onto this same pattern:
- **Scheduled** — a fixed day/time window (e.g. Monday 07:00–14:00). Built and validated:
  `detectors.tank_replenish` in `config.yaml`.
- **Event-triggered** — the window opens when the VLM itself detects a visual cue (e.g. a
  vat rotated 180° signaling a batch change), then runs for a fixed duration afterward
  (e.g. 2 hours). Designed and state-machine-tested, but the detection prompts themselves
  are not yet validated against real footage — see [Validation status](#validation-status)
  below.

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

**Why local-first**: this started as an open question (local vs. an AWS backend calling
Twilio). Decision: everything - including the Twilio API call - runs on the local
machine for now. Simpler, no hosting cost, no network dependency for the core detection
loop. The tradeoff is the one in the diagram above: if the local machine itself goes
down (crash, power outage), nothing pages anyone. That's explicitly future work (the
"Watchdog" box), not solved today.

## Validation status

Built in stages, each validated before moving to the next rather than assumed correct:

**Verified with reproducible tests:**
- **Model + prompt reliability**: 10/10 on a curated test set spanning positive, negative,
  and adversarial cases (e.g. "person pouring water, but not into the tank" - checks the
  model isn't just pattern-matching to "any pouring"). 200/200 identical outputs across
  repeated runs at temp=0, confirming determinism, not luck. Also A/B tested two model
  generations and two quantization levels to isolate what actually drove the accuracy
  gain (a training-recipe difference, not precision) before locking in the final choice.
- **Tank replenishment detector**: full state machine (deadline tracking, single-fire
  notification, day/window gating) validated end-to-end via Go integration tests
  (`backend/detector_test.go`) against the real inference pipeline, not mocked.
- **Cross-platform build**: Go binary builds natively on macOS and cross-compiles cleanly
  to Windows from the same source tree; a live Windows build/test run is in progress as
  a second independent validation pass.

**Designed and unit-tested, staged for field validation next:**
- **Event-triggered detector pattern** (the VAT/batch-change use case): state machine
  logic fully unit-tested against a mocked model; the vision prompts are first drafts
  awaiting real reference photos from the target cameras before the same validation pass
  the tank detector already went through.
- **RTSP capture and Twilio notification**: implemented directly against each API (no
  SDK dependencies), exercised against controlled failure conditions (bad URLs, missing
  credentials) to confirm graceful degradation rather than silent failure. Full
  validation against a live camera and live Twilio account is the next step once camera
  hardware is on site.

## Model approach

Currently running **InternVL3.5-2B**, quantized to **Q4_K_M** (~1.2GB) via `llama.cpp`,
zero-shot (prompted, not fine-tuned). This was chosen after direct comparison testing:

- The *previous* generation (InternVL3, non-3.5) failed our test set regardless of
  quantization level (both Q4 and near-lossless Q8 got basic cases wrong).
- InternVL3.5-2B at the same quantization passed cleanly - confirming the gap was a
  training-recipe generational difference, not a quantization-precision one.
- Prompt design matters more than model size within a generation: adding an explicit
  "what is the person doing" field before the yes/no answer measurably improved accuracy
  on ambiguous frames, reproducibly across 10 repeated runs both ways.

**Planned next step**: fine-tune a smaller (~1B) InternVL checkpoint via **LoRA** once
there's a real bank of labeled footage from the actual deployed cameras. Practically:
- Collect real frames from the live cameras, labeled with the correct field values
  (ideally 50-200+ examples per class, including known hard cases).
- Fine-tune the HuggingFace-format checkpoint (not the GGUF one) using InternVL's
  official `internvl_chat` LoRA training scripts.
- Merge the LoRA weights, then re-convert to GGUF via `convert_hf_to_gguf.py` +
  `llama-quantize` - same deployment pipeline used for the current model.

Not done yet - zero-shot 2B is already meeting the bar on available test data; revisit
once there's real-world footage to train and evaluate against.

## Triggers & actions

| | Available now | Future |
|---|---|---|
| **Triggers** | Scheduled window (day + start hour + deadline hour) | Event-triggered window (visual cue starts an N-hour countdown) - designed, not yet wired into the Go service |
| **Detection** | Local zero-shot VLM (InternVL3.5-2B) | Fine-tuned local VLM (~1B, LoRA) once labeled data exists |
| **Actions** | Twilio SMS/WhatsApp to one configured number; local speaker alarm (tone + spoken message x3) | USB-triggered physical alarm/beacon; web dashboard for status + config changes; cloud backend for richer notification routing; watchdog service that pages if the local machine itself goes offline |

## Configuration

Copy `config.yaml.example` to `config.yaml` and fill in real values - `config.yaml` is
gitignored since it holds the Twilio auth token.

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

`config.yaml` is re-read on every check, so edits take effect without restarting the
service.

## Running it

Requires `llama-mtmd-cli` (from `llama.cpp`) and `ffmpeg` on PATH, and the model files
present under `gguf/` (see `gguf/v35/` for the exact files used).

```bash
cd backend
go build -o ../sanddune .
cd ..
./sanddune   # finds config.yaml, gguf/, prompts/, state/ next to the binary itself,
                 # regardless of what directory it's launched from
```

Cross-compiling a Windows build (buildable from macOS without a Windows machine present -
see [Validation status](#validation-status) for what's confirmed on real Windows hardware
vs. cross-compiled only):

```bash
cd backend
GOOS=windows GOARCH=amd64 go build -o ../sanddune.exe .
```
Then copy `sanddune.exe` alongside `config.yaml`, `gguf/`, `prompts/`, plus Windows
builds of `llama-mtmd-cli.exe` and `ffmpeg.exe` on PATH.

To test the detection logic without a live camera, use the Go integration test (bypasses
RTSP via an image file):

```bash
cd backend
go test -v ./...
```

## Roadmap

Beyond the field validation already noted above:

- **Single-binary packaging**: bundle `ffmpeg` and `llama.cpp` into the Go executable via
  `go:embed` (embed the prebuilt platform binary as data, extract to a temp dir on first
  run) so deployment is one file plus a model-weights folder. Model weights stay separate
  regardless of packaging - same approach every local-LLM tool (Ollama, LM Studio, etc.)
  takes, since weights need to be swappable without a rebuild.
- **Multi-camera fan-out**: currently one camera per detector; generalize to N.
- **Service supervision**: a watchdog that pages the admin if the local machine itself
  goes offline (crash, power loss) - the one failure mode local-only architecture can't
  self-report on.
- **Web dashboard + cloud backend**: status visibility and config changes without
  touching the config file directly (see Architecture diagram).
- **USB-triggered physical alarm/beacon**: an additional local alert channel beyond
  speakers and phone notifications.
