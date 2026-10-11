# clef-shadow

Parallel tank-activity detector using Cloudflare **Clef** on Workers AI. Runs **alongside**
sanddune as its own launchd job; it only *reads* the same camera and posts to the same
Telegram chat with every message prefixed `[Clef test]`. It never writes sanddune's files.

## Logic (inside the schedule window) — every layer is Clef on Workers AI
| Layer | Model | When | What |
|---|---|---|---|
| Gate | **clef-flash** | every 30 s while idle | "is a person visible?" ≥ 0.5 → start a visit |
| Layer 1 | **clef-omni** | every 5 s during a visit | frame + tank-top crop → 9 observable questions (persons, hands at opening, hose/rod held, …) |
| Visit end | clef-omni + clef-flash | | 30 s with no person (omni says none **and** the gate model confirms) |
| Layer 2 | **clef** (27B) | at each visit end, and at the deadline | reads **today's whole Layer 1 timeline** (visits marked) → measurement / refill / none probabilities |
| Result | | | measurement or refill ≥ 0.8 → Telegram `[Clef test]` + best frame, stop for the day; deadline without one → "no tank activity seen" |

If the Layer 2 call fails, the old rule decides (≥ 3 frames in the visit with hands at the opening and a
hose/rod held ≥ 0.6; rod-vs-hose decides the type). Thresholds come from a handful of test clips — this is a trial.
Layer 2 reads the whole day so a visit split by a person hiding behind the tanks is still judged as one.

## Files
- `clef_shadow.py` — everything (stdlib + ffmpeg; `/usr/bin/python3` 3.9 is enough)
- `config.json` — camera URL, schedule, Telegram, thresholds (re-read every cycle)
- `.env` — `CLOUDFLARE_ACCOUNT_ID`, `CLOUDFLARE_AUTH_TOKEN` (Workers AI permission) — never commit
- `install.sh` — installs the launchd job `com.sid.clef-shadow`
- `records/<date>/` — every call (`calls.jsonl`) + the frames used, for review
- `state/<date>.json` — "started"/"finished" flags so a restart never re-sends messages

## Install on the sanddune machine
1. Put the folder **outside** Documents/Desktop/Downloads (macOS blocks launchd there), e.g. `~/clef-shadow`.
2. Fill `config.json` from sanddune's `config.yaml` (read it, don't change it):
   - `rtsp_url` ← `detectors.tank_replenish.rtsp_url`
   - `aspect_fix_width_scale` ← `detectors.tank_replenish.capture.aspect_fix_width_scale` (null if absent)
   - `schedule` ← `detectors.tank_replenish.schedule`
   - `telegram.bot_token` ← `notifications.telegram.bot_token`; `telegram.chat_ids` ← its `chat_id` / `chat_ids`
3. `python3 clef_shadow.py selftest` → expect `OK camera`, `OK gate`, `OK active`.
   Open `selftest_crop.jpg`: it should show the **tops of the round tanks**. If not, adjust
   `tank_top_crop_fraction` ([x, y, w, h] as fractions of the frame).
4. `python3 clef_shadow.py selftest --notify` → one `[Clef test]` message arrives in the chat.
5. `./install.sh` → `state = running`. Log: `logs/clef_shadow.log`.

Remove: `launchctl bootout gui/$(id -u)/com.sid.clef-shadow && rm ~/Library/LaunchAgents/com.sid.clef-shadow.plist`

## Camera streams
Each layer can use its own stream: `gate.rtsp_url` / `gate.aspect_fix_width_scale` and
`active.rtsp_url` / `active.aspect_fix_width_scale` (fall back to the top-level keys). The URL and aspect
fix travel together: the main stream (960x1080) needs `2.0`, a non-anamorphic sub-stream needs `null`.
**Keep the gate on the main stream:** tested on recorded frames, clef-flash at 352x288 missed a person
carrying the hose (0.02 vs 0.76 at 1024 wide) and a small person at the frame edge (0.30 vs 0.85).
Frames are capped at `send_width` and never upscaled. A failed grab is retried once (the DVR throws
transient "453 Not Enough Bandwidth" under concurrent pulls).

## Cost (window 05:00-15:00, gate every 30 s)
| Part | Calls/day | Tokens/call | Neurons/day |
|---|---|---|---|
| Gate, clef-flash (3,455 Neurons/M) | 1,200 | ~700 | ~2,900 |
| Layer 1, clef-omni (13,636 Neurons/M) | per visit: 12/min of presence | ~2,400 | ~33 per frame |
| Layer 2, clef (21,818 Neurons/M) | once per visit end | ~1k + ~85 per observation | ~75 for a 3-min visit |
Free allocation is 10,000 Neurons/day: ~7k left after the gate ≈ 200+ active frames (~17 min of
people present). On the Free plan, calls beyond it fail (the log shows it); on Paid they cost $0.011/1k Neurons.
