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

# Expand partition 4 to fill remaining space
log "Expanding partition table..."
parted -s "$ROOT_DEV" resizepart 4 100%

# Resize the data filesystem
log "Resizing ext4 filesystem..."
DATA_PART="${ROOT_DEV}p4"
resize2fs "$DATA_PART"

# Create data directory structure
log "Creating data directory structure..."
mkdir -p /data/.firmware-data
mkdir -p /data/recordings
mkdir -p /data/assets
chown -R observer:observer /data

# Mark expansion as complete
touch "$MARKER_FILE"
log "Data partition expansion complete"
