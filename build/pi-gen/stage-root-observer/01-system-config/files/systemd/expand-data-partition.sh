#!/bin/bash
# Expand data partition to fill remaining SD card space
# Runs once on first boot

set -e

MARKER_FILE="/data/.partition-expanded"
LOG_TAG="expand-data-partition"

log() {
    logger -t "$LOG_TAG" "$1"
    echo "$1"
}

# Skip if already completed
if [ -f "$MARKER_FILE" ]; then
    log "Data partition already expanded, skipping"
    exit 0
fi

log "Starting data partition expansion..."

# Get the root device (e.g., /dev/mmcblk0)
ROOT_DEV="/dev/mmcblk0"
DATA_PART="${ROOT_DEV}p4"

# Expand partition 4 to fill remaining space
log "Expanding partition table..."
parted -s "$ROOT_DEV" resizepart 4 100%

# Check and resize the data filesystem
log "Running filesystem check..."
e2fsck -f -y "$DATA_PART"

log "Resizing ext4 filesystem..."
if resize2fs "$DATA_PART" 2>&1 | grep -q "Nothing to do"; then
    log "Filesystem already at maximum size"
else
    log "Filesystem resized successfully"
fi

# Mount to temp location to create marker file (avoid /data to not conflict with systemd data.mount)
TEMP_MNT=$(mktemp -d)
mount "$DATA_PART" "$TEMP_MNT"
touch "$TEMP_MNT/.partition-expanded"
umount "$TEMP_MNT"
rmdir "$TEMP_MNT"

log "Data partition expansion complete"
