#!/bin/bash
# Extract a partition from a disk image
#
# Usage: ./extract-partition.sh <input.img> <output.img> <partition_number>
#
# A/B layout: 2 = Boot A, 5 = Root A (see repartition-image.sh)
#
# Prerequisites: parted, GNU dd

set -e

INPUT_IMG="$1"
OUTPUT_IMG="$2"
PARTITION="$3"

if [ -z "$INPUT_IMG" ] || [ -z "$OUTPUT_IMG" ] || [ -z "$PARTITION" ]; then
    echo "Usage: $0 <input.img> <output.img> <partition_number>"
    exit 1
fi

if [ ! -f "$INPUT_IMG" ]; then
    echo "Error: Input image not found: $INPUT_IMG"
    exit 1
fi

echo "Extracting partition $PARTITION from $INPUT_IMG..."

# Get partition info using parted
PARTITION_INFO=$(parted -s "$INPUT_IMG" unit B print | grep "^ ${PARTITION} " || true)

if [ -z "$PARTITION_INFO" ]; then
    echo "Error: Partition $PARTITION not found in image"
    parted -s "$INPUT_IMG" print
    exit 1
fi

# Parse start and size (parted output: " 5  1612709888B  6981419007B  5368709120B  logical  ext4")
START_BYTES=$(echo "$PARTITION_INFO" | awk '{print $2}' | tr -d 'B')
END_BYTES=$(echo "$PARTITION_INFO" | awk '{print $3}' | tr -d 'B')
SIZE_BYTES=$((END_BYTES - START_BYTES + 1))

echo "  Partition $PARTITION:"
echo "    Start: $START_BYTES bytes"
echo "    End:   $END_BYTES bytes"
echo "    Size:  $SIZE_BYTES bytes ($((SIZE_BYTES / 1024 / 1024)) MB)"

# Extract partition using dd
echo ""
echo "Extracting partition..."
dd if="$INPUT_IMG" of="$OUTPUT_IMG" bs=4M iflag=skip_bytes,count_bytes skip="$START_BYTES" count="$SIZE_BYTES" status=progress

# Verify the extracted image
echo ""
echo "Verifying extracted partition..."
file "$OUTPUT_IMG"

# Calculate hash
SHA256=$(sha256sum "$OUTPUT_IMG" | cut -d' ' -f1)
SIZE=$(stat -c%s "$OUTPUT_IMG" 2>/dev/null || stat -f%z "$OUTPUT_IMG")

echo ""
echo "Partition extracted successfully: $OUTPUT_IMG"
echo "  Size:   $SIZE bytes"
echo "  SHA256: $SHA256"
