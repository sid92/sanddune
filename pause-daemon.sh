#!/bin/bash
# Stops sanddune WITHOUT removing it from launchd - use this before editing
# config.yaml or rebuilding the binary. Once installed as a LaunchDaemon,
# `kill` alone doesn't work: KeepAlive means launchd just relaunches it
# immediately. bootout is the actual way to stop it.
set -e
LABEL="com.sanddune.daemon"
PLIST="/Library/LaunchDaemons/${LABEL}.plist"

if [ ! -f "$PLIST" ]; then
  echo "not installed (${PLIST} doesn't exist) - nothing to pause"
  exit 0
fi

sudo launchctl bootout system "$PLIST"
echo "Paused. Resume with ./resume-daemon.sh"
