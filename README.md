# sanddune

Local SOP Alarm System

A local, camera-based compliance watchdog. It samples RTSP camera feeds on an interval,
runs each frame through a local vision-language model, and fires an alert if an expected
event *hasn't* happened by a deadline — e.g. "the tank wasn't refilled by 2pm" or "the
floor wasn't cleaned within 2 hours of a batch change."

No cloud dependency for the detection loop itself: capture, inference, and notification
all happen on the local machine. This is a prototype/side project — architecture favors
"ship something that works" over correctness-at-scale.

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
  are not yet validated against real footage — see [Status](#status) below.

## Architecture

```mermaid
flowchart LR
    subgraph cam[Camera]
      Cam[IP Camera] -- RTSP --> FF
    end
    subgraph local["Local machine (Go service, single binary)"]
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

## Status

| Piece | State |
|---|---|
| RTSP frame grab (`ffmpeg`) | Built, not yet tested against a real camera (no camera reachable on the dev network so far) |
| Local VLM inference (`llama-mtmd-cli`) | Built and validated - 10/10 on a curated test set, 200/200 deterministic on repeat runs |
| Tank replenishment detector (scheduled trigger) | Built and validated end-to-end, including the deadline/notify/no-double-notify state machine (Go integration tests in `backend/detector_test.go`) |
| VAT/broom cleanup detector (event trigger) | State machine designed and unit-tested (mocked model) in Python (`pipelines/detector_vat_cleanup.py`); **not yet ported to the Go service**, and its detection prompts (`prompts/vat_flip.txt`, `prompts/broom_cleaning.txt`) are unvalidated first drafts - `vat_flip.txt` has a literal `[TODO]` where the VAT's normal resting orientation needs to be described from real reference photos |
| Twilio notification | Built (direct REST call, no SDK), verified against dry-run/log-only path only - not yet sent a real message |
| Local speaker alarm | macOS: built and confirmed working (`afplay` + `say`). Windows: built via a separate `alarm_windows.go` (PowerShell `System.Speech`), compiles and cross-compiles cleanly but **unverified** - no Windows machine available to confirm actual audio output |
| Windows deployment | Go binary cross-compiles cleanly (`GOOS=windows go build`); `ffmpeg`/`llama.cpp` both ship official Windows binaries. Nothing in the path-resolution or detection logic is platform-specific. Not yet run end-to-end on a real Windows machine |
| Config file | `config.yaml` (YAML) - no UI |
| Web GUI | Built, then deliberately deleted - out of scope for this phase (see Future) |

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

Cross-compiling a Windows build (no Windows machine needed to build it, just to run it -
and note the Windows local-alarm path is unverified, see Status above):

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

## Known gaps

- RTSP grabbing has never been exercised against a real camera - only against a
  deliberately-broken URL (confirmed it fails gracefully and keeps retrying rather than
  crashing the service).
- Twilio sending has never been exercised against a real account - only the dry-run/log
  path is verified.
- The VAT/broom (event-triggered) detector exists as a validated *design* but isn't part
  of the running Go service yet, and its prompts need the same real-image validation
  pass the tank detector already went through.
- Windows: the binary cross-compiles cleanly and nothing else in the codebase is
  platform-specific, but the whole thing (including the PowerShell-based local alarm)
  has never actually been run on a Windows machine.
- Still requires separate `ffmpeg` and `llama.cpp` installs alongside the Go binary.
  These could be bundled into the same executable via `go:embed` (embed the prebuilt
  platform binary as data, extract to a temp dir on first run, shell out to the extracted
  copy) - real technique, moderate effort, would need a separate embedded copy per target
  OS. Model weights (~1.8GB) should stay as separate files regardless, matching how every
  other local-LLM tool (Ollama, LM Studio, etc.) handles this - swappable without a
  rebuild, and a multi-GB binary is bad ergonomics either way. Not done yet.
- Single global camera per detector - no multi-camera fan-out yet.
- No supervision of the service itself - if the process dies or the machine loses power,
  nothing notices (see Watchdog in Future work).
