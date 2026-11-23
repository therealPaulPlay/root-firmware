#!/bin/bash
# Expand data partition to fill remaining SD card space
# Only does work on first boot or after an interrupted resize

set -e

LOG_TAG="expand-data-partition"
DISK_NAME="mmcblk0"
DISK="/dev/${DISK_NAME}"
EXTENDED_NR=4
DATA_NR=7
DATA_PART="${DISK}p${DATA_NR}"

SECTORS_PER_MIB=2048
# resize2fs drops a too small last block group
FS_TOLERANCE_BYTES=$((64 * 1024 * 1024))

log() {
    logger -t "$LOG_TAG" "$1"
    echo "$1"
}

fs_field() {
    dumpe2fs -h "$DATA_PART" 2>/dev/null | sed -n "s/^$1: *//p"
}

fs_size_bytes() {
    local blocks block_size
    blocks=$(fs_field "Block count")
    block_size=$(fs_field "Block size")
    echo $((blocks * block_size))
}

part_size_bytes() {
    blockdev --getsize64 "$DATA_PART"
}

# Kernel's view in 512-byte sectors
unpartitioned_sectors() {
    local disk_size data_start data_size
    disk_size=$(cat "/sys/block/${DISK_NAME}/size")
    data_start=$(cat "/sys/class/block/${DISK_NAME}p${DATA_NR}/start")
    data_size=$(cat "/sys/class/block/${DISK_NAME}p${DATA_NR}/size")
    echo $((disk_size - data_start - data_size))
}

if [ "$(unpartitioned_sectors)" -lt "$SECTORS_PER_MIB" ]; then
    log "Data partition already expanded"
else
    log "Starting data partition expansion..."

    # Data is the last partition inside extended
    # parted refuses while root is mounted
    log "Expanding extended partition..."
    echo ",+" | sfdisk -N "$EXTENDED_NR" --no-reread --no-tell-kernel "$DISK"

    log "Expanding data partition..."
    parted -s "$DISK" resizepart "$DATA_NR" 100%

    if [ "$(unpartitioned_sectors)" -ge "$SECTORS_PER_MIB" ]; then
        log "Data partition did not expand"
        exit 1
    fi
fi

# Checked separately to finish an interrupted resize
PART_BYTES=$(part_size_bytes)
FS_BYTES=$(fs_size_bytes)

if [ $((PART_BYTES - FS_BYTES)) -lt "$FS_TOLERANCE_BYTES" ]; then
    log "Filesystem already fills the data partition, skipping"
    exit 0
fi

# resize2fs requires a recent check
log "Running filesystem check..."
fsck_status=0
e2fsck -f -y "$DATA_PART" || fsck_status=$?

# Exit code 1 means errors were corrected
if [ "$fsck_status" -gt 1 ]; then
    log "Filesystem check failed (exit $fsck_status), not resizing"
    exit 1
fi

log "Resizing ext4 filesystem..."
resize2fs "$DATA_PART"
log "Filesystem resized successfully"

log "Data partition expansion complete"
