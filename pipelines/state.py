import json
from pathlib import Path
from typing import Any

from pipelines.config import STATE_DIR


def load(name: str) -> dict[str, Any]:
    path = STATE_DIR / f"{name}.json"
    if not path.exists():
        return {}
    return json.loads(path.read_text())


def save(name: str, data: dict[str, Any]) -> None:
    path = STATE_DIR / f"{name}.json"
    path.write_text(json.dumps(data, indent=2, default=str))
