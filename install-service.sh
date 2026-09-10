#!/bin/bash
# Installs sanddune as a macOS LaunchAgent so it starts at login, restarts if
# it crashes, and keeps the Mac awake while it runs.
#
#   ./install-service.sh            # verify, then install and start
#   ./install-service.sh --force    # install even if selftest fails
#
# LaunchAgent (per-user, starts at login) rather than LaunchDaemon (system,
# starts at boot) on purpose: the local speaker alarm shells out to `say` and
# `afplay`, and neither produces audible sound from a root daemon with no
# GUI session attached. A silent alarm is worse than no alarm, since nobody
# finds out it's silent until the day it matters. The cost is that the Mac
# has to reach a logged-in desktop - see "Surviving a reboot" in the README.
set -e
cd "$(dirname "$0")"

LABEL="com.sanddune.monitor"
PROJECT_DIR="$(pwd)"
PLIST_DEST="$HOME/Library/LaunchAgents/$LABEL.plist"
TEMPLATE="deploy/$LABEL.plist.template"
FORCE=0
[ "$1" = "--force" ] && FORCE=1

fail() { echo "ERROR: $*" >&2; exit 1; }

[ -f "$TEMPLATE" ] || fail "$TEMPLATE not found - run this from the project root."
[ -x "./sanddune" ] || fail "./sanddune not built. Run ./setup-mac.sh first."
[ -f "config.yaml" ] || fail "config.yaml not found. Run ./setup-mac.sh, then edit config.yaml."

if grep -q "camera-ip" config.yaml; then
  fail "config.yaml still has the placeholder rtsp_url. Edit it before installing the service."
fi

# Homebrew's location differs by architecture, and launchd won't find it on
# its own. Resolve it here rather than guessing in the template.
if command -v brew >/dev/null 2>&1; then
  BREW_BIN="$(brew --prefix)/bin"
else
  fail "Homebrew not found - ffmpeg and llama.cpp come from it. Run ./setup-mac.sh first."
fi
[ -x "$BREW_BIN/ffmpeg" ] || fail "ffmpeg not found in $BREW_BIN. Run ./setup-mac.sh first."

# macOS will happily register a LaunchAgent and then decline to ever start it.
# Low Power Mode defers "non-demand" spawns (RunAtLoad and KeepAlive both
# count), which shows up in `launchctl print` as
#   pended nondemand spawn = inefficient
# and looks exactly like a working install right up until the service is
# needed and was never running. Verified on this hardware: with Low Power
# Mode on, a killed service did not come back after three minutes, and a
# trivial control job never started at all.
echo "=== Checking power settings ==="
LOWPOWER="$(pmset -g 2>/dev/null | awk '/powermode/{print $2}')"
ON_BATTERY=0
pmset -g ps 2>/dev/null | head -1 | grep -q "Battery Power" && ON_BATTERY=1

if [ "$LOWPOWER" = "1" ]; then
  echo ""
  echo "  Low Power Mode is ON. macOS will defer this service indefinitely -"
  echo "  it will look installed and never actually run."
  echo ""
  echo "  Turn it off:  System Settings > Battery > Low Power Mode > Never"
  echo "  or:           sudo pmset -a lowpowermode 0"
  echo ""
  [ "$FORCE" = "1" ] || fail "refusing to install into a state where the OS won't start the service (--force to override)"
  echo "  continuing anyway because --force was given."
else
  echo "  Low Power Mode: off"
fi

if [ "$ON_BATTERY" = "1" ]; then
  echo "  WARNING: running on battery. Keep this Mac plugged in - the service"
  echo "  needs to be awake through the whole monitoring window, and macOS is"
  echo "  more aggressive about deferring background work on battery."
else
  echo "  Power: plugged in"
fi

echo ""
echo "=== Verifying the install works before supervising it ==="
if ./sanddune selftest -notify=false; then
  echo "selftest passed."
elif [ "$FORCE" = "1" ]; then
  echo "selftest FAILED - continuing anyway because --force was given."
else
  echo ""
  fail "selftest failed (see above). Fix it first, or re-run with --force if you know why.
Installing a service that can't reach the camera or the model just means it
fails silently every minute instead of once, in a log nobody is reading."
fi

mkdir -p logs "$HOME/Library/LaunchAgents"

echo ""
echo "=== Installing $LABEL ==="
sed -e "s|__LABEL__|$LABEL|g" \
    -e "s|__PROJECT_DIR__|$PROJECT_DIR|g" \
    -e "s|__BREW_BIN__|$BREW_BIN|g" \
    "$TEMPLATE" > "$PLIST_DEST"
echo "wrote $PLIST_DEST"

# Unload any previous copy first, so re-running this is an upgrade rather
# than an "already loaded" error. Both forms are tried: bootout/bootstrap are
# current, load/unload are deprecated but still what older macOS accepts.
launchctl bootout "gui/$UID/$LABEL" 2>/dev/null || launchctl unload "$PLIST_DEST" 2>/dev/null || true
launchctl bootstrap "gui/$UID" "$PLIST_DEST" 2>/dev/null || launchctl load "$PLIST_DEST"

sleep 2
if launchctl list | grep -q "$LABEL"; then
  echo ""
  echo "=== Running ==="
  launchctl list | grep "$LABEL" | awk '{print "  PID " $1 "  last exit " $2 "  " $3}'
else
  fail "service did not start - check logs/sanddune.log"
fi

cat <<EOF

Installed and started. It will now start automatically at login and restart
if it crashes.

  tail -f logs/sanddune.log        watch it work
  launchctl list | grep sanddune   check it's alive (PID, last exit code)
  ./uninstall-service.sh           stop it and remove it

The Mac stays awake while the service runs (caffeinate). Closing the lid on a
laptop still sleeps it regardless - see "Surviving a reboot" in the README.
EOF
