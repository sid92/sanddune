#!/bin/bash
# Pulls the latest code and rebuilds the binaries in place. Does not touch
# config.yaml, gguf/, or state/ - those are gitignored and stay local.
# You still need to stop the running ./sanddune and start it again yourself.
set -e
cd "$(dirname "$0")"

echo "=== Pulling latest ==="
git pull

echo "=== Rebuilding ==="
cd backend
go build -o ../sanddune .
go build -o ../cameracheck ./cmd/cameracheck
cd ..
chmod +x ./sanddune ./cameracheck

echo ""
echo "Updated. Restart it: stop the running ./sanddune, then run it again."
