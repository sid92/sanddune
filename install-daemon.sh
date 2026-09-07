#!/bin/bash
# Installs sanddune as a macOS LaunchDaemon: starts on boot, restarts
# automatically if it crashes, and keeps running through user logout and
# sleep/wake - unlike a per-user LaunchAgent, which dies on logout. Requires
# sudo since LaunchDaemons live in /Library, a system-wide location.
set -e
cd "$(dirname "$0")"
PROJECT_DIR="$(pwd)"
LABEL="com.sanddune.daemon"
PLIST_DEST="/Library/LaunchDaemons/${LABEL}.plist"
USERNAME="$(whoami)"

if [ ! -f "./sanddune" ]; then
  echo "error: ./sanddune binary not found - build it first:"
  echo "  cd backend && go build -o ../sanddune . && cd .."
  exit 1
fi
if [ ! -f "./config.yaml" ]; then
  echo "error: config.yaml not found - copy config.yaml.example to config.yaml and fill it in first"
  exit 1
fi

echo "Installing ${LABEL}"
echo "  user:   ${USERNAME}"
echo "  binary: ${PROJECT_DIR}/sanddune"
echo "  logs:   ${PROJECT_DIR}/sanddune.log"
echo "(requires sudo to write into /Library/LaunchDaemons)"

sudo tee "$PLIST_DEST" > /dev/null <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>${LABEL}</string>
    <key>ProgramArguments</key>
    <array>
        <string>${PROJECT_DIR}/sanddune</string>
    </array>
    <key>WorkingDirectory</key>
    <string>${PROJECT_DIR}</string>
    <key>UserName</key>
    <string>${USERNAME}</string>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>${PROJECT_DIR}/sanddune.log</string>
    <key>StandardErrorPath</key>
    <string>${PROJECT_DIR}/sanddune.log</string>
</dict>
</plist>
PLIST

sudo chown root:wheel "$PLIST_DEST"
sudo chmod 644 "$PLIST_DEST"

# bootout first in case it's already loaded (e.g. re-running this script
# after an edit) - errors if not loaded yet, which is fine, hence the ||true.
sudo launchctl bootout system "$PLIST_DEST" 2>/dev/null || true
sudo launchctl bootstrap system "$PLIST_DEST"

echo ""
echo "Installed and running - starts on boot, restarts on crash, survives logout/sleep."
echo ""
echo "  sudo launchctl print system/${LABEL}         # check status"
echo "  tail -f ${PROJECT_DIR}/sanddune.log          # watch logs"
echo "  ./pause-daemon.sh                            # stop temporarily (e.g. to edit config.yaml)"
echo "  ./resume-daemon.sh                           # start it again after pausing"
echo "  ./uninstall-daemon.sh                        # stop and remove entirely"
