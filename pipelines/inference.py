import re
import subprocess

from pipelines.config import MMPROJ_PATH, MODEL_PATH

_KV_LINE = re.compile(r"^([A-Z_]+):\s*(.*?)\s*$")


def run_vlm(image_path: str, prompt_path: str, n_predict: int = 150) -> dict:
    """Run the InternVL GGUF model on one image with one prompt file via llama.cpp.

    Returns a dict of the KEY: VALUE fields the model printed (e.g.
    {"PERSON_PRESENT": "YES", "POURING": "YES", "REASON": "..."}).
    Raises RuntimeError if llama-mtmd-cli fails or produces no parseable output.
    """
    result = subprocess.run(
        [
            "llama-mtmd-cli",
            "-m", MODEL_PATH,
            "--mmproj", MMPROJ_PATH,
            "--image", image_path,
            "-f", prompt_path,
            "-n", str(n_predict),
            "--temp", "0",
        ],
        capture_output=True,
        text=True,
        timeout=120,
    )
    if result.returncode != 0:
        raise RuntimeError(f"llama-mtmd-cli failed (exit {result.returncode}): {result.stderr[-2000:]}")

    fields = {}
    for line in result.stdout.splitlines():
        m = _KV_LINE.match(line.strip())
        if m:
            fields[m.group(1)] = m.group(2)

    if not fields:
        raise RuntimeError(f"No parseable KEY: VALUE output from model. Raw stdout:\n{result.stdout}")

    return fields
