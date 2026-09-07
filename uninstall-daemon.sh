#!/bin/bash
# Stops sanddune and removes it from launchd entirely (unlike
# pause-daemon.sh, which just stops it temporarily).
set -e
LABEL="com.sanddune.daemon"
PLIST="/Library/LaunchDaemons/${LABEL}.plist"

if [ ! -f "$PLIST" ]; then
  echo "not installed (${PLIST} doesn't exist)"
  exit 0
fi

sudo launchctl bootout system "$PLIST" 2>/dev/null || true
sudo rm -f "$PLIST"
echo "Stopped and removed. sanddune is no longer running as a daemon."
