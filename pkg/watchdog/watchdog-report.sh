#!/bin/sh
#
# Copyright (c) 2018 Zededa, Inc.
# SPDX-License-Identifier: Apache-2.0

# Plugged in as a watchdog repair script just so that we can record the
# watchdog reason
# Does NOT attempt any repair

# First log to /persist in case zboot/kernel is hung on disk

DATE=$(date -Is)
CURPART=$(cat /run/eve.id)
EVE_VERSION=$(cat /run/eve-release)
echo "Watchdog report for $CURPART EVE version $EVE_VERSION at $DATE: $*" >>/persist/reboot-reason
echo "$CURPART" > /persist/reboot-image
sync

# If a /run/<agent.touch> then try sending a SIGUSR1 to get a stack trace
if [ $# -ge 2 ]; then
    agent=$(echo "$2" | grep '/run/.*\.touch' | sed 's,/run/\(.*\)\.touch,\1,')
    if [ -n "$agent" ]; then
        # Map the various zedagent* to zedagent
        if [ "$agent" = "zedagentmetrics" ] -o [ "$agent" = "zedagentconfig" ] -o [ "$agent" = "zedagentdevinfo" ]; then
            agent="zedagent"
        fi
        echo "pkill -USR1 /opt/zededa/bin/$agent"
        pkill -USR1 /opt/zededa/bin/"$agent"
    fi
fi

echo "Watchdog report for $CURPART EVE version $EVE_VERSION at $DATE: $*" >>/persist/log/watchdog.log
ps >>/persist/log/watchdog.log
echo "Watchdog report done" >>/persist/log/watchdog.log

# If a /run/<agent.pid> then look for an oom message in dmesg for that agent
# and always record <agent> in reboot-reason
oom=""
agent=""
if [ $# -ge 2 ]; then
    agent=$(echo "$2" | grep '/run/.*\.pid' | sed 's,/run/\(.*\)\.pid,\1,')
    if [ -n "$agent" ]; then
        oom=$(dmesg | grep oom_reaper | grep "$agent")
    fi
fi
if [ -z "$oom" ]; then
    # Any other oom message?
    oom=$(dmesg | grep oom_reaper)
fi
if [ -z "$oom" ]; then
    # Any other oom message?
    oom=$(dmesg | grep "Out of memory")
fi
if [ -n "$oom" ]; then
   echo "$oom" >>/persist/reboot-reason
fi
if [ -n "$agent" ]; then
    echo "$agent crashed" >>/persist/reboot-reason
    panic=$(grep panic /persist/rsyslog/syslog.txt)
    if [ -n "$panic" ]; then
        echo "$panic" >>/persist/reboot-reason
        # Note that panic stack trace might exist tagged with e.g. pillar.err
        # in /persist/rsyslog/syslog.txt but can't extract from other .err
        # files.
    fi
fi

# Check if it is monitor-rsyslog.sh that crashed/stopped.
if [ $# -ge 2 ]; then
    agent=$(echo "$2" | grep '/run/.*\.pid' | sed 's,/run/\(.*\)\.pid,\1,')
    if [ "$agent" = "monitor-rsyslogd" ]; then
        rm -rf /persist/rsyslog
    fi
fi

sync
sleep 30
sync
exit 254
