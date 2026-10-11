# clef-shadow

Parallel tank-activity detector using Cloudflare **Clef** on Workers AI. Runs **alongside**
sanddune as its own launchd job; it only *reads* the same camera and posts to the same
Telegram chat with every message prefixed `[Clef test]`. It never writes sanddune's files.

## Logic (inside the schedule window)
| State | Every | Call | Next |
|---|---|---|---|
| IDLE | 10 s | 1 frame → **clef-flash**: "is a person visible?" | person ≥ 0.5 → ACTIVE |
| ACTIVE | 5 s | frame + tank-top crop → **clef-omni**, 9 observable questions | no person for 30 s → IDLE |
| DONE | – | ≥ 3 frames in one visit with `hands_at_tank_opening ≥ 0.6` **and** hose or rod held ≥ 0.6 | Telegram + best frame, stop for the day |
| DEADLINE | – | deadline hour reached without DONE | Telegram "no tank activity seen", stop for the day |

Type = average (rod − hose) over the qualifying frames: positive → measurement (rod), else refill (hose).
Thresholds come from a handful of test clips — this is a trial, expect some wrong calls.

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
