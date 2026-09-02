#!/bin/bash
set -e
cd /Users/sid/Documents/internvl-tank-check

MODEL=gguf/v35/OpenGVLab_InternVL3_5-2B-Q8_0.gguf
MMPROJ=gguf/v35/mmproj-OpenGVLab_InternVL3_5-2B-f16.gguf
IMG1=$(ls ~/Desktop/Screenshot\ 2026-08-25\ at\ 12.45.21*PM.png)
IMG2=$(ls ~/Desktop/Screenshot\ 2026-08-25\ at\ 12.45.29*PM.png)
PROMPT=gguf/prompt.txt
LOG=gguf/run_100x_raw.log
> "$LOG"

run() {
  llama-mtmd-cli -m "$MODEL" --mmproj "$MMPROJ" --image "$1" -f "$PROMPT" -n 100 --temp 0 2>/dev/null \
    | grep -E "PERSON_PRESENT|^ACTION|^POURING"
}

tally_image() {
  local img="$1"
  local label="$2"
  local n="$3"
  local person_yes=0 person_no=0
  local pour_yes=0 pour_no=0 pour_cant=0 pour_other=0

  for i in $(seq 1 "$n"); do
    out=$(run "$img")
    echo "=== $label run $i ===" >> "$LOG"
    echo "$out" >> "$LOG"

    if echo "$out" | grep -q "PERSON_PRESENT: YES"; then person_yes=$((person_yes+1));
    elif echo "$out" | grep -q "PERSON_PRESENT: NO"; then person_no=$((person_no+1)); fi

    if echo "$out" | grep -q "^POURING: YES"; then pour_yes=$((pour_yes+1));
    elif echo "$out" | grep -q "^POURING: NO"; then pour_no=$((pour_no+1));
    elif echo "$out" | grep -q "^POURING: CANNOT_TELL"; then pour_cant=$((pour_cant+1));
    else pour_other=$((pour_other+1)); fi
  done

  echo "----- $label ($n runs) -----"
  echo "PERSON_PRESENT: YES=$person_yes  NO=$person_no"
  echo "POURING:        YES=$pour_yes  NO=$pour_no  CANNOT_TELL=$pour_cant  OTHER/unparsed=$pour_other"
  echo ""
}

tally_image "$IMG1" "IMAGE1(pouring)" 100
tally_image "$IMG2" "IMAGE2(not pouring)" 100

echo "ALL DONE - raw output saved to $LOG"
