"""Detector 2: after a batch change (VAT rotated 180 degrees), has the area
been cleaned with a broom within 2 hours?

State machine per call, persisted in state/vat_cleanup.json:
  idle     -> watch for VAT_FLIP. On FLIPPED=YES, record trigger time, go to pending.
  pending  -> watch for broom cleaning. On CLEANING=YES, go to idle (resolved).
              If VAT_CLEANUP_WINDOW_HOURS elapses first, send one notification
              and move to cooldown (so we don't re-notify every subsequent call).
  cooldown -> watch for the VAT to be back in its normal (FLIPPED=NO) orientation
              before re-arming to idle, so the same still-flipped VAT doesn't
              immediately re-trigger a new pending window.

NOTE: prompts/vat_flip.txt has an unfilled placeholder describing the VAT's
normal orientation, and neither prompt here has been tested against any real
image. Both need the same validation pass detector 1's prompt went through
before this should be trusted.
"""

import argparse
import datetime
import logging
import tempfile

from pipelines import state
from pipelines.camera import grab_frame
from pipelines.config import PROMPTS_DIR, RTSP_URL_VAT, VAT_CLEANUP_WINDOW_HOURS
from pipelines.inference import run_vlm
from pipelines.notify import send_notification

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
logger = logging.getLogger("detector_vat_cleanup")

STATE_NAME = "vat_cleanup"
FLIP_PROMPT_PATH = str(PROMPTS_DIR / "vat_flip.txt")
CLEAN_PROMPT_PATH = str(PROMPTS_DIR / "broom_cleaning.txt")


def _frame(image_override: str | None) -> str:
    if image_override:
        return image_override
    path = tempfile.mktemp(suffix=".jpg")
    grab_frame(RTSP_URL_VAT, path)
    return path


def check(now: datetime.datetime, image_override: str | None = None) -> dict:
    st = state.load(STATE_NAME)
    phase = st.get("phase", "idle")

    if phase == "idle":
        fields = run_vlm(_frame(image_override), FLIP_PROMPT_PATH)
        logger.info("[idle] flip check: %s", fields)
        if fields.get("FLIPPED") == "YES":
            st = {"phase": "pending", "trigger_time": now.isoformat(), "notified": False}
            logger.info("VAT flip detected, starting %sh cleanup window", VAT_CLEANUP_WINDOW_HOURS)

    elif phase == "pending":
        trigger_time = datetime.datetime.fromisoformat(st["trigger_time"])
        elapsed_hours = (now - trigger_time).total_seconds() / 3600

        fields = run_vlm(_frame(image_override), CLEAN_PROMPT_PATH)
        logger.info("[pending, %.2fh elapsed] cleaning check: %s", elapsed_hours, fields)

        if fields.get("CLEANING") == "YES":
            logger.info("Cleaning detected, resolved")
            st = {"phase": "idle"}
        elif elapsed_hours >= VAT_CLEANUP_WINDOW_HOURS and not st["notified"]:
            send_notification(
                f"[VAT cleanup check] Area has NOT been cleaned within "
                f"{VAT_CLEANUP_WINDOW_HOURS}h of the batch change (triggered {st['trigger_time']})."
            )
            st["notified"] = True
            st["phase"] = "cooldown"

    elif phase == "cooldown":
        fields = run_vlm(_frame(image_override), FLIP_PROMPT_PATH)
        logger.info("[cooldown] waiting for VAT to return to normal: %s", fields)
        if fields.get("FLIPPED") == "NO":
            st = {"phase": "idle"}

    state.save(STATE_NAME, st)
    return {"phase": st.get("phase", "idle"), "state": st}


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--image", help="Use a local image instead of grabbing from RTSP (for testing)")
    parser.add_argument("--now", help="Override the current time for testing, ISO format")
    args = parser.parse_args()

    now = datetime.datetime.fromisoformat(args.now) if args.now else datetime.datetime.now()
    result = check(now, image_override=args.image)
    print(result)
