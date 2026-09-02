"""Send a notification via Twilio SMS/WhatsApp.

Stays in dry-run (log only, nothing sent) until TWILIO_TO_NUMBER and the
Twilio credentials are actually filled in in .env - this makes it safe to
run the detectors end-to-end during testing without risking a real message
going out to a placeholder/wrong number.
"""

import logging

from pipelines.config import (
    NOTIFY_CHANNEL,
    TWILIO_ACCOUNT_SID,
    TWILIO_AUTH_TOKEN,
    TWILIO_FROM_NUMBER,
    TWILIO_TO_NUMBER,
)

logger = logging.getLogger("notify")


def _configured() -> bool:
    return bool(
        TWILIO_ACCOUNT_SID
        and TWILIO_AUTH_TOKEN
        and TWILIO_FROM_NUMBER
        and TWILIO_TO_NUMBER
        and TWILIO_TO_NUMBER != "TBD"
    )


def send_notification(message: str) -> bool:
    """Returns True if an actual message was sent, False if it was a dry-run log."""
    if not _configured():
        logger.warning(
            "[DRY RUN - Twilio not configured or TWILIO_TO_NUMBER is still 'TBD'] "
            "Would send %s notification: %s",
            NOTIFY_CHANNEL,
            message,
        )
        return False

    from twilio.rest import Client

    client = Client(TWILIO_ACCOUNT_SID, TWILIO_AUTH_TOKEN)
    from_ = TWILIO_FROM_NUMBER
    to = TWILIO_TO_NUMBER
    if NOTIFY_CHANNEL == "whatsapp":
        from_ = f"whatsapp:{from_}"
        to = f"whatsapp:{to}"

    client.messages.create(body=message, from_=from_, to=to)
    logger.info("Sent %s notification to %s: %s", NOTIFY_CHANNEL, to, message)
    return True
