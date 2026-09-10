#!/bin/bash
# Pulls the latest code and rebuilds the binaries in place. Does not touch
# config.yaml, gguf/, or state/ - those are gitignored and stay local.
#
# If the LaunchAgent is installed, this restarts it too. Rebuilding without
# restarting leaves the old binary running against new code on disk, which
# looks like the update silently did nothing.
set -e
cd "$(dirname "$0")"

LABEL="com.sanddune.monitor"
PLIST_DEST="$HOME/Library/LaunchAgents/$LABEL.plist"

echo "=== Pulling latest ==="
git pull

echo "=== Rebuilding ==="
cd backend
go build -o ../sanddune .
go build -o ../cameracheck ./cmd/cameracheck
cd ..
chmod +x ./sanddune ./cameracheck

if [ -f "$PLIST_DEST" ]; then
  echo "=== Restarting the service ==="
  launchctl kickstart -k "gui/$UID/$LABEL" 2>/dev/null || {
    launchctl bootout "gui/$UID/$LABEL" 2>/dev/null || true
    launchctl bootstrap "gui/$UID" "$PLIST_DEST"
  }
  sleep 2
  if launchctl list | grep -q "$LABEL"; then
    launchctl list | grep "$LABEL" | awk '{print "  running, PID " $1 " (last exit " $2 ")"}'
  else
    echo "  WARNING: service did not come back - check logs/sanddune.log"
  fi
  echo ""
  echo "Updated and restarted. Watch it: tail -f logs/sanddune.log"
else
  echo ""
  echo "Updated. No LaunchAgent installed, so nothing was restarted -"
  echo "stop the running ./sanddune and start it again, or run ./install-service.sh"
fi
