#!/bin/bash
# Supervisor wrapper: runs sanddune and restarts it if it ever exits.
#
# This exists because launchd's KeepAlive could not be made to restart the
# service reliably. On the machine this was built on, a killed service never
# came back - not on battery, not on AC, not with Low Power Mode off, and not
# after Background Task Management showed the agent as "enabled, allowed".
# launchd reported `pended nondemand spawn = inefficient` and left the job at
# state = not running. A minimal control agent running nothing but /bin/sleep
# behaved identically, which ruled out anything specific to this plist.
#
# launchctl kickstart - an on-demand spawn - works every time. So the
# LaunchAgent's job is reduced to starting this script once, and keeping the
# process alive is handled here in a plain loop that cannot be deferred by
# power management. KeepAlive stays set in the plist as a backstop for the
# case where even this script dies.
set -u
cd "$(dirname "$0")/.."

BACKOFF=5
MAX_BACKOFF=60

while true; do
    START=$(date +%s)
    ./sanddune
    CODE=$?
    RAN=$(( $(date +%s) - START ))

    # A process that ran a decent while and then died is a one-off; reset the
    # backoff so it comes straight back. One that dies immediately is likely
    # misconfigured, and hammering it just floods the log - back off instead,
    # leaving something readable behind.
    if [ "$RAN" -ge 60 ]; then
        BACKOFF=5
    else
        BACKOFF=$(( BACKOFF * 2 ))
        [ "$BACKOFF" -gt "$MAX_BACKOFF" ] && BACKOFF=$MAX_BACKOFF
    fi

    echo "$(date '+%Y/%m/%d %H:%M:%S') supervisor: sanddune exited with code $CODE after ${RAN}s - restarting in ${BACKOFF}s"
    sleep "$BACKOFF"
done
