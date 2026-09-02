import os
from pathlib import Path

from dotenv import load_dotenv

PROJECT_ROOT = Path(__file__).resolve().parent.parent
load_dotenv(PROJECT_ROOT / ".env")

# InternVL model - validated InternVL3.5-2B Q8_0 via llama.cpp (see gguf/ for provenance)
MODEL_PATH = os.environ.get(
    "INTERNVL_MODEL_PATH",
    str(PROJECT_ROOT / "gguf/v35/OpenGVLab_InternVL3_5-2B-Q4_K_M.gguf"),
)
MMPROJ_PATH = os.environ.get(
    "INTERNVL_MMPROJ_PATH",
    str(PROJECT_ROOT / "gguf/v35/mmproj-OpenGVLab_InternVL3_5-2B-f16.gguf"),
)

# Camera sources - separate cameras/locations per detector (fill in real RTSP URLs)
RTSP_URL_TANK = os.environ.get("RTSP_URL_TANK", "TBD")
RTSP_URL_VAT = os.environ.get("RTSP_URL_VAT", "TBD")

# Twilio - notification stays a no-op/log-only until these are filled in
TWILIO_ACCOUNT_SID = os.environ.get("TWILIO_ACCOUNT_SID", "")
TWILIO_AUTH_TOKEN = os.environ.get("TWILIO_AUTH_TOKEN", "")
TWILIO_FROM_NUMBER = os.environ.get("TWILIO_FROM_NUMBER", "")
TWILIO_TO_NUMBER = os.environ.get("TWILIO_TO_NUMBER", "TBD")
NOTIFY_CHANNEL = os.environ.get("NOTIFY_CHANNEL", "sms")  # "sms" or "whatsapp"

STATE_DIR = PROJECT_ROOT / "state"
STATE_DIR.mkdir(exist_ok=True)

PROMPTS_DIR = PROJECT_ROOT / "prompts"

# Detector 1: tank replenishment window
TANK_CHECK_DAY = int(os.environ.get("TANK_CHECK_DAY", "0"))  # Monday = 0 (datetime.weekday())
TANK_WINDOW_START_HOUR = int(os.environ.get("TANK_WINDOW_START_HOUR", "7"))
TANK_DEADLINE_HOUR = int(os.environ.get("TANK_DEADLINE_HOUR", "14"))

# Detector 2: VAT flip -> cleanup deadline
VAT_CLEANUP_WINDOW_HOURS = float(os.environ.get("VAT_CLEANUP_WINDOW_HOURS", "2"))
