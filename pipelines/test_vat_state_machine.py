"""Sanity-check the detector_vat_cleanup state machine with a mocked model,
independent of the (unvalidated) VAT-flip/broom-cleaning prompt accuracy.
"""

import datetime
from unittest.mock import patch

from pipelines import state
from pipelines.detector_vat_cleanup import STATE_NAME, check

t0 = datetime.datetime(2026, 8, 31, 10, 0, 0)


def reset():
    state.save(STATE_NAME, {})


def run(label, now, mocked_fields, expected_phase):
    with patch("pipelines.detector_vat_cleanup.run_vlm", return_value=mocked_fields):
        result = check(now, image_override="dummy.jpg")
    ok = result["phase"] == expected_phase
    print(f"{'OK ' if ok else 'FAIL'} {label}: got phase={result['phase']!r} state={result['state']} (expected {expected_phase!r})")
    assert ok, f"{label} failed"


reset()
run("idle, no flip", t0, {"FLIPPED": "NO"}, "idle")
run("idle -> flip detected -> pending", t0, {"VAT_VISIBLE": "YES", "FLIPPED": "YES"}, "pending")
run("pending, 1h in, not cleaned -> still pending", t0 + datetime.timedelta(hours=1), {"CLEANING": "NO"}, "pending")
run("pending, 1.5h in, cleaned -> resolved to idle", t0 + datetime.timedelta(hours=1.5), {"CLEANING": "YES"}, "idle")

reset()
run("idle -> flip detected -> pending (2nd scenario)", t0, {"VAT_VISIBLE": "YES", "FLIPPED": "YES"}, "pending")
run(
    "pending, 2.5h in, not cleaned -> breach, notify, cooldown",
    t0 + datetime.timedelta(hours=2.5),
    {"CLEANING": "NO"},
    "cooldown",
)
run("cooldown, still flipped -> stays cooldown", t0 + datetime.timedelta(hours=3), {"FLIPPED": "YES"}, "cooldown")
run("cooldown, no double-notify check", t0 + datetime.timedelta(hours=3.1), {"FLIPPED": "YES"}, "cooldown")
run("cooldown -> back to normal -> idle (re-armed)", t0 + datetime.timedelta(hours=4), {"FLIPPED": "NO"}, "idle")

print("\nAll state machine transitions passed.")
