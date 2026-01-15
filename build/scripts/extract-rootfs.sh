#!/bin/bash
# Extract rootfs partition from a disk image
#
# Usage: sudo ./extract-rootfs.sh <input.img> <output-rootfs.img> [partition_number]
#
# Default partition number is 2 (standard Raspberry Pi layout)
# For A/B images, partition 2 is Root A, partition 3 is Root B
#
# Prerequisites: parted, losetup, dd
# Note: Requires sudo/root for loop device operations

set -e

INPUT_IMG="$1"
OUTPUT_ROOTFS="$2"
PARTITION="${3:-2}"

if [ -z "$INPUT_IMG" ] || [ -z "$OUTPUT_ROOTFS" ]; then
    echo "Usage: sudo $0 <input.img> <output-rootfs.img> [partition_number]"
    echo ""
    echo "partition_number defaults to 2 (Root A in A/B layout)"
    exit 1
fi

if [ ! -f "$INPUT_IMG" ]; then
    echo "Error: Input image not found: $INPUT_IMG"
    exit 1
fi

if [ "$(id -u)" -ne 0 ]; then
    echo "Error: This script requires root privileges (loop devices)"
    echo "Usage: sudo $0 <input.img> <output-rootfs.img> [partition_number]"
    exit 1
fi

echo "Extracting partition $PARTITION from $INPUT_IMG..."

# Get partition info using parted
PARTITION_INFO=$(parted -s "$INPUT_IMG" unit B print | grep "^ ${PARTITION} ")

if [ -z "$PARTITION_INFO" ]; then
    echo "Error: Partition $PARTITION not found in image"
    parted -s "$INPUT_IMG" print
    exit 1
fi

# Parse start and size (parted output: " 2  536870912B  4831838207B  4294967296B  ext4")
START_BYTES=$(echo "$PARTITION_INFO" | awk '{print $2}' | tr -d 'B')
END_BYTES=$(echo "$PARTITION_INFO" | awk '{print $3}' | tr -d 'B')
SIZE_BYTES=$((END_BYTES - START_BYTES + 1))

echo "  Partition $PARTITION:"
echo "    Start: $START_BYTES bytes"
echo "    End:   $END_BYTES bytes"
echo "    Size:  $SIZE_BYTES bytes ($((SIZE_BYTES / 1024 / 1024)) MB)"

# Extract partition using dd
echo ""
echo "Extracting rootfs partition..."
dd if="$INPUT_IMG" of="$OUTPUT_ROOTFS" bs=1M skip=$((START_BYTES / 1024 / 1024)) count=$((SIZE_BYTES / 1024 / 1024)) status=progress

# Verify the extracted image
echo ""
echo "Verifying extracted rootfs..."
file "$OUTPUT_ROOTFS"

# Calculate hash
SHA256=$(sha256sum "$OUTPUT_ROOTFS" | cut -d' ' -f1)
SIZE=$(stat -c%s "$OUTPUT_ROOTFS" 2>/dev/null || stat -f%z "$OUTPUT_ROOTFS")

echo ""
echo "Rootfs extracted successfully: $OUTPUT_ROOTFS"
echo "  Size:   $SIZE bytes"
echo "  SHA256: $SHA256"
