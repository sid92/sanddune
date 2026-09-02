#!/bin/bash
cd /Users/sid/Documents/Projects/internvl-tank-check

MODEL=gguf/v35/OpenGVLab_InternVL3_5-2B-Q4_K_M.gguf
MMPROJ=gguf/v35/mmproj-OpenGVLab_InternVL3_5-2B-f16.gguf
PROMPT=gguf/prompt.txt

DESKTOP1=$(ls "$HOME"/Desktop/Screenshot\ 2026-08-25\ at\ 12.45.21*PM.png)
DESKTOP2=$(ls "$HOME"/Desktop/Screenshot\ 2026-08-25\ at\ 12.45.29*PM.png)

declare -a images=(
  "$DESKTOP1|desktop1 - person pouring bucket into tank|YES|YES"
  "$DESKTOP2|desktop2 - person near bottles, not pouring|YES|NO"
  "test_images/img01.jpg|img01 - painting a barrel, not pouring|YES|NO"
  "test_images/img02.jpg|img02 - empty tank on truck, no person|NO|NO"
  "test_images/img03.jpg|img03 - tank + distant unrelated people|YES|NO"
  "test_images/img04.jpg|img04 - person standing idle by tank|YES|NO"
  "test_images/img05.jpg|img05 - tank only, no person|NO|NO"
  "test_images/img06.jpg|img06 - reservoir, no person|NO|NO"
  "test_images/img07.jpg|img07 - pouring into a pot (any pouring counts)|YES|YES"
  "test_images/img08.jpg|img08 - pouring into a bottle (any pouring counts)|YES|YES"
)

for entry in "${images[@]}"; do
  IFS='|' read -r path note exp_person exp_pour <<< "$entry"
  echo "=== $note [expect PERSON=$exp_person POURING=$exp_pour] ==="
  llama-mtmd-cli -m "$MODEL" --mmproj "$MMPROJ" --image "$path" -f "$PROMPT" -n 100 --temp 0 2>/dev/null \
    | grep -E "PERSON_PRESENT|^ACTION|^POURING"
  echo ""
done

echo "ALL DONE"
