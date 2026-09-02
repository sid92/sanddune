#!/bin/bash
set -e
cd /Users/sid/Documents/internvl-tank-check

MODEL=gguf/v35/OpenGVLab_InternVL3_5-2B-Q8_0.gguf
MMPROJ=gguf/v35/mmproj-OpenGVLab_InternVL3_5-2B-f16.gguf
IMG1=$(ls ~/Desktop/Screenshot\ 2026-08-25\ at\ 12.45.21*PM.png)
IMG2=$(ls ~/Desktop/Screenshot\ 2026-08-25\ at\ 12.45.29*PM.png)
PROMPT=gguf/prompt_no_action.txt

run() {
  llama-mtmd-cli -m "$MODEL" --mmproj "$MMPROJ" --image "$1" -f "$PROMPT" -n 100 --temp 0 2>/dev/null \
    | grep -E "PERSON_PRESENT|^POURING"
}

echo "##### 10x IMAGE 1 (pouring), NO ACTION field #####"
for i in $(seq 1 10); do
  echo "--- run $i ---"
  run "$IMG1"
done

echo ""
echo "##### 10x IMAGE 2 (not pouring), NO ACTION field #####"
for i in $(seq 1 10); do
  echo "--- run $i ---"
  run "$IMG2"
done

echo ""
echo "ALL DONE"
