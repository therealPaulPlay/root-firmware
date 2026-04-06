#!/bin/bash
# Expand data partition to fill remaining SD card space
# Runs once on first boot

set -e

LOG_TAG="expand-data-partition"
ROOT_DEV="/dev/mmcblk0"
DATA_PART="${ROOT_DEV}p4"

log() {
    logger -t "$LOG_TAG" "$1"
    echo "$1"
}

# Check if partition 4 already ends at the disk boundary (within 1MB tolerance)
DISK_SIZE=$(blockdev --getsize64 "$ROOT_DEV")
PART_END=$(parted -s "$ROOT_DEV" unit B print | awk '$1 == "4" {print $3}' | tr -d 'B')

if [ -n "$PART_END" ] && [ $((DISK_SIZE - PART_END)) -lt 1048576 ]; then
    log "Data partition already expanded, skipping"
    exit 0
fi

log "Starting data partition expansion..."

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

log "Data partition expansion complete"
