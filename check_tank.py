"""
Ask InternVL whether a water tank in an image looks filled.

Usage:
    python check_tank.py path/to/image1.jpg [path/to/image2.jpg ...]
    python check_tank.py --model OpenGVLab/InternVL3-2B-hf path/to/image.jpg
"""

import argparse
import sys

import torch
from transformers import AutoModelForImageTextToText, AutoProcessor

DEFAULT_MODEL = "OpenGVLab/InternVL3-8B-hf"

PROMPT = (
    "This is a still frame from a fixed camera pointed at a water tank. Frames are captured "
    "every 5 seconds; you are only judging this single frame.\n\n"
    "First, determine whether a person is visible anywhere in the frame.\n"
    "If a person is present, briefly describe what they appear to be doing (e.g. carrying a "
    "bucket, standing idle, walking away, working with tools, etc.).\n"
    "Then, determine whether they are actively pouring water - e.g. holding, lifting, or "
    "tipping a bucket, jug, or other container so that water is visibly flowing or about to "
    "flow out of it. This counts regardless of what the water is being poured into (the tank "
    "or any other container).\n\n"
    "Do NOT try to judge the water level or contents inside the tank - it is not visible to "
    "the camera. Only judge whether a person is present, what they are doing, and whether "
    "they are pouring.\n\n"
    "Respond in exactly this format:\n"
    "PERSON_PRESENT: <YES | NO>\n"
    "ACTION: <brief phrase describing what the person is doing, or NONE if no person is present>\n"
    "POURING: <YES | NO | CANNOT_TELL>\n"
    "REASON: <one sentence on what you actually observed - person present/absent, bucket/jug "
    "position, water visibly flowing, or why you couldn't tell>\n\n"
    "If PERSON_PRESENT is NO, ACTION must be NONE and POURING must be NO."
)


def load(model_checkpoint: str):
    device = "mps" if torch.backends.mps.is_available() else "cpu"
    dtype = torch.float16 if device == "mps" else torch.float32
    print(f"Loading {model_checkpoint} on {device} ({dtype})...", file=sys.stderr)

    processor = AutoProcessor.from_pretrained(model_checkpoint)
    model = AutoModelForImageTextToText.from_pretrained(
        model_checkpoint,
        dtype=dtype,
        attn_implementation="sdpa",
    ).to(device)
    model.eval()
    return processor, model


def check_image(processor, model, image_path: str, prompt: str = PROMPT) -> str:
    messages = [
        {
            "role": "user",
            "content": [
                {"type": "image", "url": image_path} if image_path.startswith("http")
                else {"type": "image", "path": image_path},
                {"type": "text", "text": prompt},
            ],
        }
    ]
    inputs = processor.apply_chat_template(
        messages,
        add_generation_prompt=True,
        tokenize=True,
        return_dict=True,
        return_tensors="pt",
    ).to(model.device)

    with torch.no_grad():
        generate_ids = model.generate(**inputs, max_new_tokens=100, do_sample=False)

    output = processor.decode(
        generate_ids[0, inputs["input_ids"].shape[1]:], skip_special_tokens=True
    )
    return output.strip()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("images", nargs="+", help="Image file paths or URLs")
    parser.add_argument("--model", default=DEFAULT_MODEL, help="HF checkpoint to use")
    parser.add_argument("--prompt", default=None, help="Override the default prompt")
    args = parser.parse_args()

    processor, model = load(args.model)

    for image_path in args.images:
        print(f"\n=== {image_path} ===")
        result = check_image(processor, model, image_path, args.prompt or PROMPT)
        print(result)


if __name__ == "__main__":
    main()
