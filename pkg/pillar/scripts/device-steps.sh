#!/bin/sh
#
# Copyright (c) 2018 Zededa, Inc.
# SPDX-License-Identifier: Apache-2.0

WATCHDOG_PID=/run/watchdog/pid
WATCHDOG_FILE=/run/watchdog/file
CONFIGDIR=/config
PERSISTDIR=/persist
PERSIST_RKT_DATA_DIR=$PERSISTDIR/rkt
PERSIST_RKT_CNI_DIR=$PERSISTDIR/rkt-cni
BINDIR=/opt/zededa/bin
TMPDIR=/persist/tmp
ZTMPDIR=/var/tmp/zededa
DPCDIR=$ZTMPDIR/DevicePortConfig
FIRSTBOOTFILE=$ZTMPDIR/first-boot
GCDIR=$PERSISTDIR/config/GlobalConfig
LOGDIRA=$PERSISTDIR/IMGA/log
LOGDIRB=$PERSISTDIR/IMGB/log
AGENTS0="logmanager ledmanager nim nodeagent"
AGENTS1="zedmanager zedrouter domainmgr downloader verifier identitymgr zedagent baseosmgr wstunnelclient"
AGENTS="$AGENTS0 $AGENTS1"
TPM_DEVICE_PATH="/dev/tpmrm0"

PATH=$BINDIR:$PATH

echo "$(date -Ins -u) Starting device-steps.sh"
echo "$(date -Ins -u) EVE version: $(cat $BINDIR/versioninfo)"

MEASURE=0
while [ $# != 0 ]; do
    if [ "$1" = -m ]; then
        MEASURE=1
    elif [ "$1" = -w ]; then
        echo "$(date -Ins -u) Got old -w"
    else
        echo "Usage: device-steps.sh [-h] [-m]"
        exit 1
    fi
    shift
done

# Sleep for a bit until /var/run/$1.touch exists
wait_for_touch() {
    f=/var/run/"$1".touch
    waited=0
    while [ ! -f "$f" ] && [ "$waited" -lt 60 ]; do
            echo "$(date -Ins -u) waiting for $f"
            sleep 3
            waited=$((waited + 3))
    done
    if [ ! -f "$f" ]; then
        echo "$(date -Ins -u) gave up waiting for $f"
    else
        echo "$(date -Ins -u) waited $waited for $f"
    fi
}

mkdir -p $ZTMPDIR
if [ -d $TMPDIR ]; then
    echo "$(date -Ins -u) Old TMPDIR files:"
    ls -lt $TMPDIR
    rm -rf $TMPDIR
fi
mkdir -p $TMPDIR
export TMPDIR

if ! mount -o remount,flush,dirsync,noatime $CONFIGDIR; then
    echo "$(date -Ins -u) Remount $CONFIGDIR failed"
fi

DIRS="$CONFIGDIR $ZTMPDIR $CONFIGDIR/DevicePortConfig"

for d in $DIRS; do
    d1=$(dirname "$d")
    if [ ! -d "$d1" ]; then
        echo "$(date -Ins -u) Create $d1"
        mkdir -p "$d1"
        chmod 700 "$d1"
    fi
    if [ ! -d "$d" ]; then
        echo "$(date -Ins -u) Create $d"
        mkdir -p "$d"
        chmod 700 "$d"
    fi
done

echo "$(date -Ins -u) Configuration from factory/install:"
(cd $CONFIGDIR || return; ls -l)
echo

# Make sure we have a v2tlsbaseroot-certificates.pem for the V2 API. If none was in /config
# from the installer we pick the one from Alpine. This ensures that updated systems have a
# useful file in place.
# NOTE: The V2 API does not trust the /config/root-certificates.pem for TLS, however
# that file expresses the root for the trust in the signed configuration.
# We also make sure that we have this file in /persist/certs/ under a sha-based name.
# Finally, the currently used base file is indicated by the content of
# /persist/certs/v2tlsbaseroot-certificates.sha256. This is to prepare for a future
# feature where the controller can update the base file.
# Note that programatically we add any proxy certificates to the list of roots we trust.
if [ ! -f /config/v2tlsbaseroot-certificates.pem ]; then
    echo "$(date -Ins -u) Creating default /config/v2tlsbaseroot-certificates.pem"
    cp -p /etc/ssl/certs/ca-certificates.crt /config/v2tlsbaseroot-certificates.pem
fi
sha=$(openssl sha256 /config/v2tlsbaseroot-certificates.pem | awk '{print $2}')
if [ ! -f "/persist/certs/$sha" ]; then
    echo "$(date -Ins -u) Adding /config/v2tlsbaseroot-certificates.pem to /persist/certs"
    cp /config/v2tlsbaseroot-certificates.pem "/persist/certs/$sha"
fi
if [ ! -f /persist/certs/v2tlsbaseroot-certificates.sha256 ]; then
    echo "$(date -Ins -u) Setting /config/v2tlsbaseroot-certificates.pem as current"
    echo "$sha" >/persist/certs/v2tlsbaseroot-certificates.sha256
fi

CONFIGDEV=$(zboot partdev CONFIG)

if [ ! -d $LOGDIRA ]; then
    echo "$(date -Ins -u) Creating $LOGDIRA"
    mkdir -p $LOGDIRA
fi
if [ ! -d $LOGDIRB ]; then
    echo "$(date -Ins -u) Creating $LOGDIRB"
    mkdir -p $LOGDIRB
fi

P3=$(/hostfs/sbin/findfs PARTLABEL=P3)
P3_FS_TYPE=$(blkid "$P3"| awk '{print $3}' | sed 's/TYPE=//' | sed 's/"//g')
if [ -c $TPM_DEVICE_PATH ] && ! [ -f $CONFIGDIR/disable-tpm ] && [ "$P3_FS_TYPE" = "ext4" ]; then
    #It is a device with TPM, and formatted with ext4, setup fscrypt
    echo "$(date -Ins -u) EXT4 partitioned $PERSISTDIR, enabling fscrypt"
    #Initialize fscrypt algorithm, hash length etc.
    $BINDIR/vaultmgr -c "$CURPART" setupVaults
fi

# FIXME: remove these two once we get rid of rkt (or move it to xen-tools)
if [ ! -d "$PERSIST_RKT_DATA_DIR" ]; then
    echo "$(date -Ins -u) Create $PERSIST_RKT_DATA_DIR"
    mkdir -p "$PERSIST_RKT_DATA_DIR"
    chmod 700 "$PERSIST_RKT_DATA_DIR"
fi
if [ ! -d "$PERSIST_RKT_CNI_DIR" ]; then
    echo "$(date -Ins -u) Create $PERSIST_RKT_CNI_DIR"
    mkdir -p "$PERSIST_RKT_CNI_DIR"
    chmod 744 "$PERSIST_RKT_CNI_DIR"
fi

if [ -f $PERSISTDIR/IMGA/reboot-reason ]; then
    echo "IMGA reboot-reason: $(cat $PERSISTDIR/IMGA/reboot-reason)"
fi
if [ -f $PERSISTDIR/IMGB/reboot-reason ]; then
    echo "IMGB reboot-reason: $(cat $PERSISTDIR/IMGB/reboot-reason)"
fi
if [ -f $PERSISTDIR/reboot-reason ]; then
    echo "Common reboot-reason: $(cat $PERSISTDIR/reboot-reason)"
fi

echo "$(date -Ins -u) Current downloaded files:"
ls -lt $PERSISTDIR/downloads/*/*
echo

echo "$(date -Ins -u) Preserved images:"
ls -lt $PERSISTDIR/img/
echo

# Copy any GlobalConfig from /config
dir=$CONFIGDIR/GlobalConfig
for f in "$dir"/*.json; do
    if [ "$f" = "$dir/*.json" ]; then
        break
    fi
    if [ ! -d $GCDIR ]; then
        mkdir -p $GCDIR
    fi
    echo "$(date -Ins -u) Copying from $f to $GCDIR"
    cp -p "$f" $GCDIR
done

if ! CURPART=$(zboot curpart); then
    CURPART="IMGA"
fi

if [ ! -d $PERSISTDIR/log ]; then
    echo "$(date -Ins -u) Creating $PERSISTDIR/log"
    mkdir $PERSISTDIR/log
fi

# BlinkCounter 1 means we have started; might not yet have IP addresses
# client/selfRegister and zedagent update this when the found at least
# one free uplink with IP address(s)
mkdir -p /var/tmp/zededa/LedBlinkCounter/
echo '{"BlinkCounter": 1}' > '/var/tmp/zededa/LedBlinkCounter/ledconfig.json'

# If ledmanager is already running we don't have to start it.
# TBD: Should we start it earlier before wwan and wlan services?
if ! pgrep ledmanager >/dev/null; then
    echo "$(date -Ins -u) Starting ledmanager"
    ledmanager &
    wait_for_touch ledmanager
fi
echo "$(date -Ins -u) Starting nodeagent"
$BINDIR/nodeagent -c $CURPART &
wait_for_touch nodeagent

mkdir -p "$WATCHDOG_PID" "$WATCHDOG_FILE"
touch "$WATCHDOG_PID/nodeagent.pid" "$WATCHDOG_FILE/nodeagent.touch" \
      "$WATCHDOG_PID/ledmanager.pid" "$WATCHDOG_FILE/ledmanager.touch"

mkdir -p $DPCDIR

# Look for a USB stick with a usb.json file
# XXX note that gpt on the USB stick needs to be labeled with DevicePortConfig
# If there is a dump directory on the stick we put log and debug info
# in there.
# If there is an identity directory on the stick we put identifying
# information in a subdir there.
access_usb() {
    # echo "$(date -Ins -u) XXX Looking for USB stick with DevicePortConfig"
    SPECIAL=$(lsblk -l -o name,label,partlabel | awk '/DevicePortConfig|QEMU VVFAT/ {print "/dev/"$1;}')
    if [ -n "$SPECIAL" ] && [ -b "$SPECIAL" ]; then
        echo "$(date -Ins -u) Found USB with DevicePortConfig: $SPECIAL"
        if ! mount -t vfat "$SPECIAL" /mnt; then
            # XXX !? will be zero
            echo "$(date -Ins -u) mount $SPECIAL failed: $?"
            return
        fi
        for fd in "usb.json:$DPCDIR" hosts:/config server:/config ; do
            file=/mnt/$(echo "$fd" | cut -f1 -d:)
            dst=$(echo "$fd" | cut -f2 -d:)
            if [ -f "$file" ]; then
                echo "$(date -Ins -u) Found $file on $SPECIAL"
                echo "$(date -Ins -u) Copying from $file to $dst"
                cp -p "$file" "$dst"
            else
                echo "$(date -Ins -u) $file not found on $SPECIAL"
            fi
        done
        if [ -d /mnt/identity ] && [ -f $CONFIGDIR/device.cert.pem ]; then
            echo "$(date -Ins -u) Saving identity to USB stick"
            IDENTITYHASH=$(openssl sha256 $CONFIGDIR/device.cert.pem |awk '{print $2}')
            IDENTITYDIR="/mnt/identity/$IDENTITYHASH"
            [ -d "$IDENTITYDIR" ] || mkdir -p "$IDENTITYDIR"
            cp -p $CONFIGDIR/device.cert.pem "$IDENTITYDIR"
            [ ! -f $CONFIGDIR/onboard.cert.pem ] || cp -p $CONFIGDIR/onboard.cert.pem "$IDENTITYDIR"
            [ ! -f $CONFIGDIR/uuid ] || cp -p $CONFIGDIR/uuid "$IDENTITYDIR"
            cp -p $CONFIGDIR/root-certificate.pem "$IDENTITYDIR"
            [ ! -f $CONFIGDIR/v2tlsbaseroot-certificates.pem ] || cp -p $CONFIGDIR/v2tlsbaseroot-certificates.pem "$IDENTITYDIR"
            [ ! -f $CONFIGDIR/hardwaremodel ] || cp -p $CONFIGDIR/hardwaremodel "$IDENTITYDIR"
            [ ! -f $CONFIGDIR/soft_serial ] || cp -p $CONFIGDIR/soft_serial "$IDENTITYDIR"
            /opt/zededa/bin/hardwaremodel -c >"$IDENTITYDIR/hardwaremodel.dmi"
            /opt/zededa/bin/hardwaremodel -f >"$IDENTITYDIR/hardwaremodel.txt"
            sync
        fi
        if [ -d /mnt/dump ]; then
            echo "$(date -Ins -u) Dumping diagnostics to USB stick"
            # Check if it fits without clobbering an existing tar file
            if tar cf /mnt/dump/diag1.tar /persist/status/ /persist/config /var/run/ /persist/log "/persist/$CURPART/log"; then
                mv /mnt/dump/diag1.tar /mnt/dump/diag.tar
            else
                rm -f /mnt/dump/diag1.tar
            fi
            sync
        fi
        umount -f /mnt
        blockdev --flushbufs "$SPECIAL"
    fi
}

# Read any usb.json with DevicePortConfig, and deposit our identity
access_usb

# Update our local /etc/hosts with entries comming from /config
[ -f /config/hosts ] && cat /config/hosts >> /etc/hosts

# Need to clear old usb files from /config/DevicePortConfig
if [ -f $CONFIGDIR/DevicePortConfig/usb.json ]; then
    echo "$(date -Ins -u) Removing old $CONFIGDIR/DevicePortConfig/usb.json"
    rm -f $CONFIGDIR/DevicePortConfig/usb.json
fi
# Copy any DevicePortConfig from /config
dir=$CONFIGDIR/DevicePortConfig
for f in "$dir"/*.json; do
    if [ "$f" = "$dir/*.json" ]; then
        break
    fi
    echo "$(date -Ins -u) Copying from $f to $DPCDIR"
    cp -p "$f" $DPCDIR
done

# Get IP addresses
echo "$(date -Ins -u) Starting nim"
$BINDIR/nim -c $CURPART &
wait_for_touch nim

# Restart watchdog ledmanager and nim
touch "$WATCHDOG_PID/nim.pid" "$WATCHDOG_FILE/nim.touch"

# Print diag output forever on changes
$BINDIR/diag -c $CURPART -f -o /dev/console &

# Wait for having IP addresses for a few minutes
# so that we are likely to have an address when we run ntp
echo "$(date -Ins -u) Starting waitforaddr"
$BINDIR/waitforaddr -c $CURPART

# Deposit any diag information from nim
access_usb

# We need to try our best to setup time *before* we generate the certifiacte.
# Otherwise the cert may have start date in the future or in 1970
echo "$(date -Ins -u) Check for NTP config"
if [ -f /usr/sbin/ntpd ]; then
    # '-p' means peer in some distros; pidfile in others
    /usr/sbin/ntpd -q -n -p pool.ntp.org
    # Run ntpd to keep it in sync.
    /usr/sbin/ntpd -g -p pool.ntp.org
else
    echo "$(date -Ins -u) No ntpd"
fi

# The device cert generation needs the current time. Some hardware
# doesn't have a battery-backed clock
YEAR=$(date +%Y)
while [ "$YEAR" = "1970" ]; do
    echo "$(date -Ins -u) It's still 1970; waiting for ntp to advance"
    sleep 10
    YEAR=$(date +%Y)
done

# Restart watchdog ledmanager, client, and nim
touch "$WATCHDOG_PID/zedclient.pid" \
      "$WATCHDOG_PID/ntpd.pid"

if [ ! -f $CONFIGDIR/device.cert.pem ]; then
    echo "$(date -Ins -u) Generating a device key pair and self-signed cert (using TPM/TEE if available)"
    touch $FIRSTBOOTFILE # For zedagent
    touch $CONFIGDIR/self-register-pending
    sync
    blockdev --flushbufs "$CONFIGDEV"
    if [ -c $TPM_DEVICE_PATH ] && ! [ -f $CONFIGDIR/disable-tpm ]; then
        echo "$(date -Ins -u) TPM device is present and allowed, creating TPM based device key"
        if ! $BINDIR/generate-device.sh -b $CONFIGDIR/device -t; then
            echo "$(date -Ins -u) TPM is malfunctioning, falling back to software certs"
            $BINDIR/generate-device.sh -b $CONFIGDIR/device
        fi
    else
        $BINDIR/generate-device.sh -b $CONFIGDIR/device
    fi
    # Reduce chance that we register with controller and crash before
    # the filesystem has persisted /config/device.cert.* and
    # self-register-pending
    sync
    blockdev --flushbufs "$CONFIGDEV"
    sleep 10
    sync
    blockdev --flushbufs "$CONFIGDEV"
    SELF_REGISTER=1
elif [ -f $CONFIGDIR/self-register-pending ]; then
    echo "$(date -Ins -u) previous self-register failed/killed/rebooted"
    SELF_REGISTER=1
else
    echo "$(date -Ins -u) Using existing device key pair and self-signed cert"
    SELF_REGISTER=0
fi
if [ ! -f $CONFIGDIR/server ] || [ ! -f $CONFIGDIR/root-certificate.pem ]; then
    echo "$(date -Ins -u) No server or root-certificate to connect to. Done"
    exit 0
fi

# Deposit any diag information from nim and onboarding
access_usb

if [ $SELF_REGISTER = 1 ]; then
    rm -f $ZTMPDIR/zedrouterconfig.json

    # Persistently remember we haven't finished selfRegister in case the device
    # is powered off
    echo "$(date -Ins -u) Self-registering our device certificate"
    if ! [ -f $CONFIGDIR/onboard.cert.pem ] || ! [ -f $CONFIGDIR/onboard.key.pem ]; then
        echo "$(date -Ins -u) Missing onboarding certificate. Giving up"
        exit 1
    fi
    echo "$(date -Ins -u) Starting client selfRegister getUuid"
    if ! $BINDIR/client -c $CURPART selfRegister getUuid; then
        # XXX $? is always zero
        echo "$(date -Ins -u) client selfRegister failed with $?"
        exit 1
    fi
    rm -f $CONFIGDIR/self-register-pending
    sync
    blockdev --flushbufs "$CONFIGDEV"
    if [ ! -f $CONFIGDIR/hardwaremodel ]; then
        /opt/zededa/bin/hardwaremodel -c >$CONFIGDIR/hardwaremodel
        echo "$(date -Ins -u) Created default hardwaremodel $(/opt/zededa/bin/hardwaremodel -c)"
    fi
    # Make sure we set the dom0 hostname, used by LISP nat traversal, to
    # a unique string. Using the uuid
    uuid=$(cat $CONFIGDIR/uuid)
    /bin/hostname "$uuid"
    /bin/hostname >/etc/hostname
    if ! grep -q "$uuid" /etc/hosts; then
        # put the uuid in /etc/hosts to avoid complaints
        echo "$(date -Ins -u) Adding $uuid to /etc/hosts"
        echo "127.0.0.1 $uuid" >>/etc/hosts
    else
        echo "$(date -Ins -u) Found $uuid in /etc/hosts"
    fi
else
    echo "$(date -Ins -u) Get UUID in in case device was deleted and recreated with same device cert"
    echo "$(date -Ins -u) Starting client getUuid"
    $BINDIR/client -c $CURPART getUuid
    if [ ! -f $CONFIGDIR/hardwaremodel ]; then
        echo "$(date -Ins -u) XXX /config/hardwaremodel missing; creating"
        /opt/zededa/bin/hardwaremodel -c >$CONFIGDIR/hardwaremodel
        echo "$(date -Ins -u) Created hardwaremodel $(/opt/zededa/bin/hardwaremodel -c)"
    fi

    uuid=$(cat $CONFIGDIR/uuid)
    /bin/hostname "$uuid"
    /bin/hostname >/etc/hostname

    if ! grep -q "$uuid" /etc/hosts; then
        # put the uuid in /etc/hosts to avoid complaints
        echo "$(date -Ins -u) Adding $uuid to /etc/hosts"
        echo "127.0.0.1 $uuid" >>/etc/hosts
    else
        echo "$(date -Ins -u) Found $uuid in /etc/hosts"
    fi
fi

# Setup default amount of space for images
# Half of /persist by default! Convert to kbytes
size=$(df -B1 --output=size $PERSISTDIR | tail -1)
space=$((size / 2048))
mkdir -p /var/tmp/zededa/GlobalDownloadConfig/
echo \{\"MaxSpace\":"$space"\} >/var/tmp/zededa/GlobalDownloadConfig/global.json

# Remove zedclient.pid from watchdog (get back to ledmanager and nim)
rm "$WATCHDOG_PID/zedclient.pid"

for AGENT in $AGENTS1; do
    echo "$(date -Ins -u) Starting $AGENT"
    $BINDIR/"$AGENT" -c $CURPART &
    wait_for_touch "$AGENT"
done

# Start vaultmgr as a service
$BINDIR/vaultmgr -c "$CURPART" runAsService &
wait_for_touch vaultmgr
touch "$WATCHDOG_PID/vaultmgr.pid" "$WATCHDOG_FILE/vaultmgr.touch"

# Start tpmmgr as a service
if [ -c $TPM_DEVICE_PATH ] && ! [ -f $CONFIGDIR/disable-tpm ] ; then
    echo "$(date -Ins -u) Starting tpmmgr as a service agent"
    $BINDIR/tpmmgr -c "$CURPART" runAsService &
    wait_for_touch tpmmgr
    touch "$WATCHDOG_PID/tpmmgr.pid" "$WATCHDOG_FILE/tpmmgr.touch"
fi

#If logmanager is already running we don't have to start it.
if ! pgrep logmanager >/dev/null; then
    echo "$(date -Ins -u) Starting logmanager"
    $BINDIR/logmanager -c $CURPART &
    wait_for_touch logmanager
    touch "$WATCHDOG_PID/logmanager.pid" "$WATCHDOG_FILE/logmanager.touch"
fi

# Now run watchdog for all agents
for AGENT in $AGENTS; do
    touch "$WATCHDOG_PID/$AGENT.pid"
    touch "$WATCHDOG_FILE/$AGENT.touch"
    if [ "$AGENT" = "zedagent" ]; then
       touch "$WATCHDOG_FILE/${AGENT}config.touch" "$WATCHDOG_FILE/${AGENT}metrics.touch" "$WATCHDOG_FILE/${AGENT}devinfo.touch"
    fi
done

blockdev --flushbufs "$CONFIGDEV"

echo "$(date -Ins -u) Initial setup done"

if [ $MEASURE = 1 ]; then
    ping6 -c 3 -w 1000 zedcontrol
    echo "$(date -Ins -u) Measurement done"
fi

echo "$(date -Ins -u) Done starting EVE version: $(cat $BINDIR/versioninfo)"

# If there is a USB stick inserted and debug.enable.usb is set, we periodically
# check for any usb.json with DevicePortConfig, deposit our identity,
# and dump any diag information
while true; do
    access_usb
    sleep 300
done
