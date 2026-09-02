#!/bin/bash
set -e
cd /Users/sid/Documents/internvl-tank-check

MODEL=gguf/v35/OpenGVLab_InternVL3_5-2B-Q8_0.gguf
MMPROJ=gguf/v35/mmproj-OpenGVLab_InternVL3_5-2B-f16.gguf
IMG1=$(ls ~/Desktop/Screenshot\ 2026-08-25\ at\ 12.45.21*PM.png)
IMG2=$(ls ~/Desktop/Screenshot\ 2026-08-25\ at\ 12.45.29*PM.png)

run() {
  llama-mtmd-cli -m "$MODEL" --mmproj "$MMPROJ" --image "$1" -f "$2" -n 100 --temp 0 2>/dev/null \
    | grep -E "PERSON_PRESENT|^ACTION|^POURING"
}

echo "##### 10x IMAGE 1 (pouring), ACTION-before-POURING order #####"
for i in $(seq 1 10); do
  echo "--- run $i ---"
  run "$IMG1" gguf/prompt.txt
done

echo ""
echo "##### 10x IMAGE 2 (not pouring), ACTION-before-POURING order #####"
for i in $(seq 1 10); do
  echo "--- run $i ---"
  run "$IMG2" gguf/prompt.txt
done

echo ""
echo "##### SWAPPED ORDER (POURING-before-ACTION), 1x each #####"
echo "--- image 1 ---"
run "$IMG1" gguf/prompt_swapped.txt
echo "--- image 2 ---"
run "$IMG2" gguf/prompt_swapped.txt

echo ""
echo "ALL DONE"
