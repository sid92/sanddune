#!/bin/bash
# Restarts sanddune after ./pause-daemon.sh - reuses the plist already
# installed by install-daemon.sh, doesn't regenerate it. If you rebuilt the
# binary or edited config.yaml while paused, this picks up the new file
# since ProgramArguments just points at the same path either way.
set -e
LABEL="com.sanddune.daemon"
PLIST="/Library/LaunchDaemons/${LABEL}.plist"

if [ ! -f "$PLIST" ]; then
  echo "error: ${PLIST} doesn't exist - run ./install-daemon.sh first"
  exit 1
fi

sudo launchctl bootstrap system "$PLIST"
echo "Resumed."
