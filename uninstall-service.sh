#!/bin/bash
# Stops the sanddune LaunchAgent and removes it, so it no longer starts at
# login. Leaves the code, config.yaml, gguf/, state/ and logs/ untouched -
# this only removes the supervision, not the install.
set -e
cd "$(dirname "$0")"

LABEL="com.sanddune.monitor"
PLIST_DEST="$HOME/Library/LaunchAgents/$LABEL.plist"

launchctl bootout "gui/$UID/$LABEL" 2>/dev/null || launchctl unload "$PLIST_DEST" 2>/dev/null || true

if [ -f "$PLIST_DEST" ]; then
  rm "$PLIST_DEST"
  echo "removed $PLIST_DEST"
else
  echo "no LaunchAgent installed at $PLIST_DEST"
fi

if launchctl list | grep -q "$LABEL"; then
  echo "WARNING: $LABEL still appears in launchctl - try logging out and back in."
else
  echo "Stopped. sanddune will no longer start at login."
  echo "Run it by hand with ./sanddune, or reinstall with ./install-service.sh"
fi
