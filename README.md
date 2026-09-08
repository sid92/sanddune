# sanddune

Local SOP alarm system. Samples RTSP camera feeds on an interval, runs each frame through
a local vision-language model, and fires an alert if an expected event hasn't happened by
a deadline — e.g. tank not refilled by 2pm, floor not cleaned within 2 hours of a batch
change.

Capture, inference, and notification all run on the local machine. No cloud dependency
in the detection loop — streaming continuous camera footage off-site isn't bandwidth-viable
at this scale anyway.

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
  180° signaling a batch change), runs for a fixed duration after (e.g. 2 hours).

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

## Model approach

Running **InternVL3.5-2B**, quantized to **Q4_K_M** (~1.2GB) via `llama.cpp`, zero-shot
(prompted, not fine-tuned).

- The previous generation (InternVL3, non-3.5) failed the test set at any quantization
  level. InternVL3.5-2B passed cleanly at the same quantization — the gap was the
  training recipe, not precision.
- Earlier prompt iteration found wording mattered more than model size: adding a "what
  is the person doing" field before the yes/no answer improved accuracy on ambiguous
  frames. The prompt was then simplified to a single `POURING: YES/NO` question
  (person-visibility requirement dropped) - validated 4/4 against real camera crops.
- The real SOP turned out to have a case that single question couldn't represent:
  operators check the water level and only refill if it's actually needed - "checked,
  no refill needed" and "never checked at all" both look identical to a POURING-only
  question. Current prompt asks a three-way question instead (checking / pouring /
  neither) and reports both `CHECKING: YES/NO` and `POURING: YES/NO`, resolved with
  `resolve_match: any` - either signal alone satisfies the SOP, seeing both isn't
  required. Framing it as two independent yes/no questions, rather than picking
  between checking-or-pouring, mattered a lot: an earlier either/or phrasing forced the
  model to always call one of the two true even when neither was actually happening -
  19/26 real crops false-flagged as `POURING: YES` under that phrasing, 0/26 once
  "neither" became an explicit third option. Not yet validated against a real confirmed
  pour-in-progress frame with this prompt - only real checking/negative examples and the
  old synthesized test image so far. See [Validation status](#validation-status).

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

`cameracheck` also checks the device's clock against this machine's (Hikvision ISAPI
`/System/time`) - reports the UTC offset both sides are using and whether they agree, since
`backfill` (below) depends on that assumption and a mismatch would silently pull frames from
the wrong moment, exactly as happened once during development (see `backfill.go`'s comment
on `buildPlaybackURL`).

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
    check_interval_seconds: 60

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
      resolve_match: all  # all | any - see "Model approach" below

    objects:              # omit entirely for a single tank (full frame, no crop)
      - id: tank_left
        crop: [0, 0, 0, 0] # [x, y, w, h] - action region, set by eye against a real frame
      - id: tank_right
        crop: [0, 0, 0, 0]

    require: all          # all | any

    camera_health:
      interval_seconds: 30  # 24x7, independent of the schedule above - see below
      miss_threshold: 2     # consecutive failed pulls before alerting

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

### Camera health alerts

Runs 24x7 in its own goroutine, on its own fixed cadence
(`camera_health.interval_seconds`, default 30s) - fully independent of the tank detector's
schedule, since a DVR/network outage doesn't wait for business hours. Each check is one
throwaway single-frame grab, immediately discarded - deliberately lightweight since it runs
around the clock rather than only during a narrow window. After
`camera_health.miss_threshold` (default 2) consecutive failed pulls it sends one Telegram
alert, then one more when a pull succeeds again - both as the same short two-line template:

```
Time: Sat 09/06 02:59 PM
DVR offline
```

(`DVR online` on recovery). The verbose ffmpeg error behind a failure goes to the local log
only, never into the Telegram text. Neither message repeats while the state holds - one
alert per transition, not per tick, so a multi-hour outage doesn't flood the chat.

The down/up state (not the miss count) is persisted to `state/camera_health.json`
specifically so this holds across restarts too - a crash, an `update.sh`, or just manually
restarting `sanddune` while the camera is already known to be down must NOT re-send the
"offline" alert. Found this the hard way during development: several restarts against the
same real, still-ongoing outage each independently hit their own miss threshold and re-sent
"offline", even though nothing had actually changed - confusing and easy to mistake for a
flapping connection. Confirmed fixed live: a second run against the same real, still-ongoing
outage stayed silent instead of re-alerting. The "restart while down, then actually recover"
half of that same scenario is unit-tested (`TestCameraHealthPersistsAcrossRestart`) but not
yet confirmed live - would need the real outage to end while testing this specific path.

Confirmed live against a real DVR outage during development (see
[Validation status](#validation-status)).

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

Before trusting a new install (or a new camera/config on an existing one), verify the whole
chain end-to-end:

```bash
./sanddune selftest              # real camera pull + real VLM inference + real Telegram send
./sanddune selftest -notify=false          # skip the Telegram send
./sanddune selftest -object=tank_near      # only test one configured object
```

Runs the exact same code the live service uses - not a mock - against every configured
object, and saves the pulled frame plus each object's crop under `state/selftest/<timestamp>/`
so you can look at exactly what the model saw. Every check runs regardless of whether an
earlier one failed, printing a report line (`OK`/`FAIL`/`SKIP`) for each - config, camera,
one line per object's model check, and notify - so a broken camera doesn't hide whether
Telegram is configured correctly, since those are independent things to go fix. `SKIP` means
a prerequisite failed (e.g. every model check is skipped if the camera check failed - no
frame to run inference on), not that the check itself broke. Exits non-zero if anything
failed, after the full report - not before it. `./sanddune` itself also does the camera half
of this check once at startup, before entering its main loop - `selftest` covers the rest
(model, notify) and can be re-run any time, not just at startup, including forcing a real
Telegram send you'd otherwise only see on an actual deadline breach.

### Catching up after downtime

If the machine running `sanddune` was down (crashed, rebooted, asleep) during part of a
check window, `backfill` re-runs the real detection pipeline against the DVR's own
historical recording instead of the live feed - Hikvision-specific, since it relies on the
same ISAPI playback convention `cameracheck`'s clock check verifies:

```bash
./sanddune backfill -hours=2                    # last 2 hours, ending now
./sanddune backfill -start=12:00 -end=14:00      # an exact window instead, today's date
./sanddune backfill -object=tank_near             # only check one configured object
```

Stops checking an object as soon as it resolves, same as the live service, and saves every
frame/crop under `state/backfill/<timestamp>/` for inspection. Report-only: it does **not**
touch `state/tank_replenish.json`, even with the live service running at the same time
(reads config, never that state file) - decide by hand whether an object that resolved
during the gap should stop the live service from expecting it, since automatically merging
into a file the live service might be writing to at the same moment risks a race.

**Adaptive sampling** - the DVR's per-seek playback cost (see below) makes uniformly-dense
sampling over a long window impractical, so `backfill` samples at `-interval` (default:
`check_interval_seconds`) normally, and ramps down to the tighter `-fine-interval` (default
10s) the moment a separate check against the *overall* frame (not any one object's crop)
reports `-ramp-field` equals `-ramp-equals` (default `PERSON_PRESENT`/`YES`, matching
`prompts/tank_pouring.txt`) - then immediately back to the base interval the next sample
that field isn't set, no cooldown. This overall-frame check runs unconditionally alongside
the normal per-object crop checks every sample, never instead of them - what to ramp to next
doesn't change whether this sample's objects get checked. The field name is a flag, not
hardcoded, since "what means look closer" is a property of whatever prompt is configured,
not something backfill.go should assume.

Two real bugs surfaced building this, both worth knowing about before trusting it on a new
camera: the DVR silently treats query-param timestamps as its own local wall-clock time
regardless of the ISO8601 "Z" suffix (fixed - see `buildPlaybackURL`, and verify against any
new device with `cameracheck`'s clock check above rather than assuming), and playback
connections take measurably longer to establish than live ones, so `grabFrame`'s timeout is
now a parameter instead of a hardcoded value tuned only for live streaming (`-grab-timeout`,
default 30s for backfill vs. 10s live).

**Concurrency** - a full multi-hour window is a lot of individual DVR seeks at ~10-20s each
sequentially, so `-workers N` splits the time range into N contiguous chunks and runs them
as concurrent playback sessions instead. Default is 1 (sequential, unchanged from before
this existed) - raising it is a deliberate choice, not a new default, because what's
actually safe here is genuinely unclear: a brief burst test (a few seconds) showed 4
concurrent sessions completing cleanly at close to the same per-request time as 1 alone,
but a real 35-minute run at 4 workers saw a 79% grab failure rate - far worse than the
burst test predicted, and not fully explained by a ~6 minute laptop sleep that happened
mid-run (the math doesn't add up to anywhere near that many failures from sleep alone). A
short synthetic test does not reliably predict sustained real-world behavior here - treat
any concurrency number as unproven until validated over the length of run you actually
intend, not a quick check.

**Retries** - `grabFrameRetrying` retries an unbounded number of times with escalating
backoff (3s, 5s, 10s, capped at 15s) on ANY grab failure, not just a specific error code -
an earlier version only retried the DVR's `453 Not Enough Bandwidth` response, which missed
almost every real failure (a laptop sleep event produces "Network is unreachable" instead,
not 453, and was silently never retried - each one a real sample permanently lost rather
than a transient blip worth waiting out). This is a tool a human runs and can Ctrl-C, so
"eventually succeeds or the human notices it's stuck" beats a bounded retry count that
risks quietly losing samples to what's usually recoverable (sleep, wake, a brief network
drop).

**Filling gaps** - `-timestamps <file>` re-checks a specific list of `HH:MM:SS` lines
instead of scanning a `[-start,-end)` range, for topping up exactly what a previous run's
failures skipped without re-doing everything that already succeeded:

```bash
grep "grab failed" old-run.log | grep -oE "^[0-9:]+" | sort -u > gaps.txt
./sanddune backfill -timestamps=gaps.txt -workers=2 -keep-checking
```

`-workers` still splits the list across concurrent sessions (an even split by index, not by
how far apart entries happen to be in time). No ramp cadence applies, since there's no
"next sample" spacing to adjust once the exact list of times to check is already fixed.

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

## Deploying to a new machine

The repo is private, so the target machine needs its own GitHub access first — either
`gh auth login` there, or an SSH key / personal access token added to that machine's git.
After that, setup is one command:

```bash
git clone https://github.com/sid92/sanddune.git && cd sanddune && ./setup-mac.sh
```

`setup-mac.sh` installs `go`, `ffmpeg`, and `llama.cpp` via Homebrew, builds `sanddune` and
`cameracheck` from source, downloads the two model files (~1.8GB), and creates
`config.yaml` from the template if one doesn't exist yet. `config.yaml`, `gguf/`, and
`state/` are all gitignored, so they're local to each machine and never get overwritten by
a pull — every deployment needs its own crop coordinates confirmed against its own camera
(see [Objects and crops](#objects-and-crops)), since coordinates from one physical layout
don't transfer to another.

To update an existing deployment after pushing new code:

```bash
./update.sh   # git pull + rebuild; doesn't touch config.yaml, gguf/, or state/
```

This restarts nothing by itself — stop the running `./sanddune` and start it again after
updating. There's no process supervision yet (see Roadmap), so a crash or a machine reboot
currently requires someone to notice and restart it manually.

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

## Validation status

> **Sections below marked (superseded) describe the per-tank, action-classification
> design that was replaced by presence + dwell time.** They are kept because the
> measurements are real and explain why the design changed, but `tank_near`/`tank_far`,
> `POURING: YES` and per-object attribution no longer exist in `config.yaml`.

**Verified on a full day of real footage (723 frames, 2026-09-07, 09:00-14:00):**
- The model answers presence reliably and action not at all. Roughly a dozen prompt
  variants were run over the whole day. Every action-phrased prompt failed: "filling the
  tank with a bottle of water" described a person holding a bottle in a frame containing
  nobody; "checking the tank level" fired on 36/36 empty rooms while catching only 4
  scattered frames of a real 2-minute episode; "being checked or worked on" answered YES
  on all 26 frames tested.
- Naming a concrete object does not fix confabulation. "Is a person holding a water
  bottle or water container?" was asked of the 35 frames where presence was detected,
  and answered YES on 18 of them. **The site operator confirmed no refill occurred during
  any of those frames**, and all 18 were checked by eye at full resolution - no bottle,
  jug or container appears in any of them. Precision 0%. Recall is unmeasurable: the day
  contains no refill, so there is no positive case to miss. This is why the pipeline has
  no bottle step. (Reproduce: `state/bottle_results.csv`, review page
  `state/bottle_review.html` - both gitignored, they embed real footage.)
- The model cannot condition on a drawn bounding box, shown three ways: naming the box's
  colour, swapping which colour marks which tank, and stating the scene as fact all
  produced "yes to whichever box you ask about". Raising resolution to 896px and full
  1920px made it worse, not better, so this is not a detail-visibility problem.
- A physical crop works where a drawn box does not: cropping to both tanks and asking
  only about presence scored 11/12 on a hand-checked window. ffmpeg does the framing the
  model cannot.
- Dwell time as an action proxy, measured over the full day: presence fired on 35 of 723
  frames, forming 14 continuous runs. Exactly one reached 120s - 12:31:20 to 12:34:30, 20
  consecutive positives, 190s - and it is the episode confirmed by eye as two workers
  servicing the tanks. The other 13 runs were one or two frames, 30s at most. Replaying at
  the live 60s interval gives the same answer: the object resolves once, at 12:33:00.
  So on that day: 1 detection, 1 true positive, 0 false positives, and correctly no alert
  (resolving means the SOP was met - the alert only fires at the deadline if nothing did).
  Recall past that one event is not established; the day contains no second known episode
  to catch. `applyDwell` is unit-tested as a pure function (`backend/dwell_test.go`).

**Verified (infrastructure, still current):**
- Detector state machine: deadline tracking, single-fire notification, day/window
  gating, per-object resolution — tested end-to-end via Go integration tests
  (`backend/detector_test.go`) against the real inference pipeline.
- Camera health alerts: state-transition logic unit-tested (`backend/camera_health_test.go`),
  the "unreachable" side confirmed live against a real DVR outage, and cross-restart
  persistence (no duplicate "offline" alert across a restart while still down) confirmed
  live by actually restarting against that same ongoing outage. The "restart while down,
  then it recovers" path is unit-tested but not yet confirmed live (would need the real
  outage to end while testing that specific path).
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

- Real camera connected: the actual deployment camera (Hikvision DVR, channel showing
  the two-tank room). Found and fixed a real bug there - the channel transmits "1080P
  Lite" mode (half horizontal resolution, no metadata saying so), which would have made
  every crop coordinate wrong. `backend/cmd/cameracheck` now catches this automatically
  for any camera - confirmed independently on both Mac and the Windows box.
- Telegram notification: real message sent from the actual code path (bot token + chat
  ID from `getUpdates`) and confirmed received.
- Multi-object resolution (`require: all` across two objects) - state machine exercised
  end-to-end, both objects tracked independently.
- Windows crop-pipeline timing measured: ~62s for a full 2-object cycle (vs ~4.7 min/frame
  full-resolution single-object) - crop+448 is a real ~6x win there, but the absolute
  number is nowhere near the Mac's ~1.2-1.3s/crop (~25x slower hardware). ~17-19s of
  *each* call is model reload, since the CLI respawns per object - this cost multiplies
  with object count, which is why a resident-model (`llama-server`) rework is now a real
  priority rather than a nice-to-have, especially for cameras with more than 2 objects.
  `check_interval_seconds: 10` is not achievable on that hardware as currently built.

**Crop coordinates (superseded - single `tank_area` crop now, no per-tank boxes):**
- An earlier `tank_near` box (my own widened substitution, not Sid's) was confirmed
  wrong - wide enough to span *both* tanks, so a pour at either would have incorrectly
  resolved both objects. `config.yaml` now uses Sid's exact coordinates instead, read
  directly off a gridded 1920x1080 reference frame (`tank_near: [1100,0,400,1000]`,
  `tank_far: [1500,0,400,1000]`).
- These still aren't fully trustworthy. With the person-agnostic prompt, a test against
  this exact `tank_near` box on the synthesized positive image returned `POURING: NO` -
  the box may be too tight and clip the actual water/opening, not just the operator.
  `tank_far` has no positive example of its own at all. The two tanks sit immediately
  adjacent, so any box with enough headroom for one tank's pour risks clipping into the
  neighbor's space either way. Resolving this needs a real or synthesized pour-in-progress
  photo at **each** tank individually - blocking for finishing crop calibration, not a
  nice-to-have.

**Not yet field-tested:**
- Event-triggered detector (VAT/batch-change): state machine unit-tested against a
  mocked model in Python, not ported to the Go service. Prompts are first drafts.
- Our original 8-image stock test set is still weak (only one real hard negative, no
  outdoor-tank scene) - superseded for the tank detector by the real-camera validation
  above, but still what backs the Go integration tests in `backend/detector_test.go`.

