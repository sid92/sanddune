"""Detector 1: has the water tank been replenished by 2pm every Monday?

Meant to be invoked periodically (e.g. every 5s via cron/systemd timer) during
the Monday morning window. Each call:
  - does nothing outside Monday 07:00-14:00 (configurable in pipelines/config.py)
  - grabs a frame (RTSP, or --image override for testing) and checks for POURING
  - marks the day "replenished" on the first positive detection
  - once past the 14:00 deadline, sends one notification if still unreplenished

State is persisted per-day in state/tank_replenish.json so this is safe to
call repeatedly without double-notifying.
"""

import argparse
import datetime
import logging
import tempfile

from pipelines import state
from pipelines.camera import grab_frame
from pipelines.config import (
    PROMPTS_DIR,
    RTSP_URL_TANK,
    TANK_CHECK_DAY,
    TANK_DEADLINE_HOUR,
    TANK_WINDOW_START_HOUR,
)
from pipelines.inference import run_vlm
from pipelines.notify import send_notification

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
logger = logging.getLogger("detector_tank_replenish")

STATE_NAME = "tank_replenish"
PROMPT_PATH = str(PROMPTS_DIR / "tank_pouring.txt")


def check(now: datetime.datetime, image_override: str | None = None) -> dict:
    today = now.date().isoformat()
    st = state.load(STATE_NAME)

    if st.get("date") != today:
        st = {"date": today, "replenished": False, "notified": False}

    in_window = now.weekday() == TANK_CHECK_DAY and TANK_WINDOW_START_HOUR <= now.hour < 24
    past_deadline = now.weekday() == TANK_CHECK_DAY and now.hour >= TANK_DEADLINE_HOUR

    if now.weekday() != TANK_CHECK_DAY:
        state.save(STATE_NAME, st)
        return {"status": "not_monday", "state": st}

    if not st["replenished"] and now.hour < TANK_DEADLINE_HOUR and in_window:
        if image_override:
            frame_path = image_override
        else:
            frame_path = tempfile.mktemp(suffix=".jpg")
            grab_frame(RTSP_URL_TANK, frame_path)

        fields = run_vlm(frame_path, PROMPT_PATH)
        logger.info("Detection result: %s", fields)
        if fields.get("POURING") == "YES":
            st["replenished"] = True
            logger.info("Tank replenishment detected for %s", today)

    if past_deadline and not st["replenished"] and not st["notified"]:
        send_notification(
            f"[Tank check] Water tank has NOT been replenished as of "
            f"{TANK_DEADLINE_HOUR}:00 on {today}. Please check."
        )
        st["notified"] = True

    state.save(STATE_NAME, st)
    return {"status": "checked", "state": st}


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--image", help="Use a local image instead of grabbing from RTSP (for testing)")
    parser.add_argument(
        "--now",
        help="Override the current time for testing, ISO format e.g. 2026-08-31T13:00:00 (must be a Monday to exercise the window)",
    )
    args = parser.parse_args()

    now = datetime.datetime.fromisoformat(args.now) if args.now else datetime.datetime.now()
    result = check(now, image_override=args.image)
    print(result)
