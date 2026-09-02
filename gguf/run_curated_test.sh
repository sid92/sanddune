#!/bin/bash
set -e
cd /Users/sid/Documents/internvl-tank-check

MODEL=gguf/v35/OpenGVLab_InternVL3_5-2B-Q8_0.gguf
MMPROJ=gguf/v35/mmproj-OpenGVLab_InternVL3_5-2B-f16.gguf
PROMPT=gguf/prompt.txt
DIR=test_images

declare -a images=(
  "abdul_cleaning_tank.jpg|person painting a barrel, not pouring"
  "tank_filled_vehicle1.jpg|empty tank on truck, no person"
  "tank_filled_vehicle2.jpg|empty tank on truck, distant unrelated people"
  "priest_near_tank.jpg|person standing idle next to tank"
  "empty_tank_no_person.jpg|tank only, no person"
  "traditional_tank.jpg|tarp-lined reservoir, no person (ambiguous 'tank')"
  "pouring_into_pot.jpg|person pouring water, but into a pot not a tank"
  "pouring_into_bottle.jpg|person pouring water, but into a bottle not a tank"
)

for entry in "${images[@]}"; do
  fname="${entry%%|*}"
  note="${entry#*|}"
  echo "=== $fname ($note) ==="
  llama-mtmd-cli -m "$MODEL" --mmproj "$MMPROJ" --image "$DIR/$fname" -f "$PROMPT" -n 100 --temp 0 2>/dev/null \
    | grep -E "PERSON_PRESENT|^ACTION|^POURING|^REASON"
  echo ""
done

echo "ALL DONE"
