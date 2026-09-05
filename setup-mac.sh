#!/bin/bash
# One-time setup for sanddune on a new Mac, after `git clone`. Installs Go,
# ffmpeg, and llama.cpp via Homebrew, builds the binaries from source,
# downloads the model files, and creates config.yaml from the template.
#
#   git clone https://github.com/sid92/sanddune.git && cd sanddune && ./setup-mac.sh
set -e
cd "$(dirname "$0")"

echo "=== Checking Homebrew ==="
if ! command -v brew >/dev/null 2>&1; then
  echo "Homebrew not found. Install it first: https://brew.sh"
  echo '  /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"'
  exit 1
fi

echo "=== Installing go + ffmpeg + llama.cpp ==="
brew install go ffmpeg llama.cpp

echo "=== Building binaries from source ==="
cd backend
go build -o ../sanddune .
go build -o ../cameracheck ./cmd/cameracheck
cd ..
chmod +x ./sanddune ./cameracheck

echo "=== Downloading model files (~1.8GB total) ==="
mkdir -p gguf/v35
MODEL_URL="https://huggingface.co/bartowski/OpenGVLab_InternVL3_5-2B-GGUF/resolve/main/OpenGVLab_InternVL3_5-2B-Q4_K_M.gguf"
MMPROJ_URL="https://huggingface.co/bartowski/OpenGVLab_InternVL3_5-2B-GGUF/resolve/main/mmproj-OpenGVLab_InternVL3_5-2B-f16.gguf"

if [ ! -f "gguf/v35/OpenGVLab_InternVL3_5-2B-Q4_K_M.gguf" ]; then
  curl -L -o gguf/v35/OpenGVLab_InternVL3_5-2B-Q4_K_M.gguf "$MODEL_URL"
else
  echo "model already present, skipping"
fi

if [ ! -f "gguf/v35/mmproj-OpenGVLab_InternVL3_5-2B-f16.gguf" ]; then
  curl -L -o gguf/v35/mmproj-OpenGVLab_InternVL3_5-2B-f16.gguf "$MMPROJ_URL"
else
  echo "mmproj already present, skipping"
fi

echo "=== Config ==="
if [ ! -f "config.yaml" ]; then
  cp config.yaml.example config.yaml
  echo "Created config.yaml from the template - EDIT IT before running:"
  echo "  - detectors.tank_replenish.rtsp_url (real camera URL)"
  echo "  - notifications.telegram.bot_token / chat_id"
  echo "  - crop coordinates under objects: (see README 'Objects and crops')"
else
  echo "config.yaml already exists, leaving it alone"
fi

echo ""
echo "=== Verifying ==="
llama-mtmd-cli --version 2>&1 | head -1
ffmpeg -version 2>&1 | head -1

echo ""
echo "Setup complete. Edit config.yaml, then run: ./sanddune"
echo "(Run ./cameracheck first against any NEW camera before trusting its proportions - see README.)"
