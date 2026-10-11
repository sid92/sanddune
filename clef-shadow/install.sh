#!/bin/bash
# Install clef-shadow as its own launchd job. Does not touch sanddune.
# Run from the clef-shadow folder after filling config.json and .env:   ./install.sh
set -euo pipefail
DIR="$(cd "$(dirname "$0")" && pwd)"
LABEL="com.sid.clef-shadow"
PLIST="$HOME/Library/LaunchAgents/$LABEL.plist"

case "$DIR" in
  "$HOME/Documents"*|"$HOME/Desktop"*|"$HOME/Downloads"*)
    echo "Refusing: $DIR is a macOS-protected folder; launchd jobs there fail silently. Move it (e.g. ~/clef-shadow)."; exit 1;;
esac
[ -f "$DIR/.env" ] || { echo "Missing $DIR/.env (CLOUDFLARE_ACCOUNT_ID, CLOUDFLARE_AUTH_TOKEN)"; exit 1; }
/usr/bin/python3 -c "import json;c=json.load(open('$DIR/config.json'));assert c['rtsp_url'] and c['telegram']['bot_token'] and c['telegram']['chat_ids'], 'fill rtsp_url and telegram in config.json'"

mkdir -p "$DIR/logs" "$HOME/Library/LaunchAgents"
cat > "$PLIST" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>$LABEL</string>
  <key>ProgramArguments</key><array>
    <string>/usr/bin/python3</string><string>$DIR/clef_shadow.py</string><string>run</string>
  </array>
  <key>WorkingDirectory</key><string>$DIR</string>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ProcessType</key><string>Interactive</string>
  <key>EnvironmentVariables</key><dict><key>PATH</key><string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin</string></dict>
  <key>StandardOutPath</key><string>$DIR/logs/clef_shadow.log</string>
  <key>StandardErrorPath</key><string>$DIR/logs/clef_shadow.log</string>
</dict></plist>
EOF

launchctl bootout "gui/$(id -u)/$LABEL" 2>/dev/null || true
launchctl bootstrap "gui/$(id -u)" "$PLIST"
launchctl kickstart -k "gui/$(id -u)/$LABEL"
sleep 3
launchctl print "gui/$(id -u)/$LABEL" | grep -E "state =|pid =" || true
echo "Installed. Log: $DIR/logs/clef_shadow.log"
echo "Remove with: launchctl bootout gui/\$(id -u)/$LABEL && rm $PLIST"
