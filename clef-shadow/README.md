# clef-shadow

Parallel tank-activity detector using Cloudflare **Clef** on Workers AI. Runs **alongside**
sanddune as its own launchd job; it only *reads* the same camera and posts to the same
Telegram chat with every message prefixed `[Clef test]`. It never writes sanddune's files.

## Logic (inside the schedule window) — every layer is Clef on Workers AI
| Layer | Model | When | What |
|---|---|---|---|
| Gate | **clef-flash** | every 10 s while idle | "is a person visible?" ≥ 0.5 → start a visit |
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

## Cost
Gate (clef-flash, ~700 tokens × 2,520 calls for 07:00–14:00) ≈ 1.8M tokens ≈ 6k Neurons;
active frames (clef-omni, ~2,400 tokens each) ≈ 33 Neurons per frame. A normal day stays near
the 10k Neurons/day free allocation; on the Free plan calls beyond it fail (the log will show it).
