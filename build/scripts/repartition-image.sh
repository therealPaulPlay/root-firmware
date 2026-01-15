#!/bin/bash
# Repartition a pi-gen image for A/B root partition layout
#
# Usage: sudo ./repartition-image.sh <input.img> <output.img>
#
# Creates a 4-partition layout:
#   1. Boot partition (512MB, FAT32)
#   2. Root A (4GB, ext4)
#   3. Root B (4GB, ext4)
#   4. Data (remaining space, ext4)
#
# Prerequisites: parted, losetup, kpartx, e2fsprogs
# Note: Requires sudo/root for loop device operations

set -e

INPUT_IMG="$1"
OUTPUT_IMG="$2"

if [ -z "$INPUT_IMG" ] || [ -z "$OUTPUT_IMG" ]; then
    echo "Usage: sudo $0 <input.img> <output.img>"
    exit 1
fi

if [ ! -f "$INPUT_IMG" ]; then
    echo "Error: Input image not found: $INPUT_IMG"
    exit 1
fi

if [ "$(id -u)" -ne 0 ]; then
    echo "Error: This script requires root privileges (loop devices)"
    echo "Usage: sudo $0 <input.img> <output.img>"
    exit 1
fi

# Sizes in MB
BOOT_SIZE_MB=512
ROOT_SIZE_MB=4096
DATA_MIN_SIZE_MB=1024

# Calculate total size needed
TOTAL_SIZE_MB=$((BOOT_SIZE_MB + ROOT_SIZE_MB * 2 + DATA_MIN_SIZE_MB))

echo "Creating A/B partition image..."
echo "  Boot:   ${BOOT_SIZE_MB}MB"
echo "  Root A: ${ROOT_SIZE_MB}MB"
echo "  Root B: ${ROOT_SIZE_MB}MB"
echo "  Data:   ${DATA_MIN_SIZE_MB}MB+ (expands on first boot)"
echo "  Total:  ${TOTAL_SIZE_MB}MB minimum"

# Track resources for cleanup
LOOP_DEV=""
INPUT_LOOP=""
MOUNT_BOOT_SRC=""
MOUNT_BOOT_DST=""
MOUNT_ROOT_SRC=""
MOUNT_ROOT_A_DST=""
MOUNT_ROOT_B_DST=""

cleanup() {
    echo "Cleaning up..."
    # Unmount any mounted filesystems
    [ -n "$MOUNT_BOOT_SRC" ] && mountpoint -q "$MOUNT_BOOT_SRC" && umount "$MOUNT_BOOT_SRC" 2>/dev/null
    [ -n "$MOUNT_BOOT_DST" ] && mountpoint -q "$MOUNT_BOOT_DST" && umount "$MOUNT_BOOT_DST" 2>/dev/null
    [ -n "$MOUNT_ROOT_SRC" ] && mountpoint -q "$MOUNT_ROOT_SRC" && umount "$MOUNT_ROOT_SRC" 2>/dev/null
    [ -n "$MOUNT_ROOT_A_DST" ] && mountpoint -q "$MOUNT_ROOT_A_DST" && umount "$MOUNT_ROOT_A_DST" 2>/dev/null
    [ -n "$MOUNT_ROOT_B_DST" ] && mountpoint -q "$MOUNT_ROOT_B_DST" && umount "$MOUNT_ROOT_B_DST" 2>/dev/null

    # Remove mount directories
    [ -n "$MOUNT_BOOT_SRC" ] && rm -rf "$MOUNT_BOOT_SRC" 2>/dev/null
    [ -n "$MOUNT_BOOT_DST" ] && rm -rf "$MOUNT_BOOT_DST" 2>/dev/null
    [ -n "$MOUNT_ROOT_SRC" ] && rm -rf "$MOUNT_ROOT_SRC" 2>/dev/null
    [ -n "$MOUNT_ROOT_A_DST" ] && rm -rf "$MOUNT_ROOT_A_DST" 2>/dev/null
    [ -n "$MOUNT_ROOT_B_DST" ] && rm -rf "$MOUNT_ROOT_B_DST" 2>/dev/null

    # Clean up loop devices
    [ -n "$INPUT_LOOP" ] && kpartx -d "$INPUT_LOOP" 2>/dev/null
    [ -n "$INPUT_LOOP" ] && losetup -d "$INPUT_LOOP" 2>/dev/null
    [ -n "$LOOP_DEV" ] && kpartx -d "$LOOP_DEV" 2>/dev/null
    [ -n "$LOOP_DEV" ] && losetup -d "$LOOP_DEV" 2>/dev/null
}
trap cleanup EXIT

# Create output image with required size
echo ""
echo "Creating image file..."
truncate -s ${TOTAL_SIZE_MB}M "$OUTPUT_IMG"

# Create partition table
echo "Creating partition table..."
parted -s "$OUTPUT_IMG" mklabel msdos

# Create partitions
# Partition 1: Boot (FAT32)
parted -s "$OUTPUT_IMG" mkpart primary fat32 1MiB ${BOOT_SIZE_MB}MiB
parted -s "$OUTPUT_IMG" set 1 boot on

# Partition 2: Root A (ext4)
parted -s "$OUTPUT_IMG" mkpart primary ext4 ${BOOT_SIZE_MB}MiB $((BOOT_SIZE_MB + ROOT_SIZE_MB))MiB

# Partition 3: Root B (ext4)
parted -s "$OUTPUT_IMG" mkpart primary ext4 $((BOOT_SIZE_MB + ROOT_SIZE_MB))MiB $((BOOT_SIZE_MB + ROOT_SIZE_MB * 2))MiB

# Partition 4: Data (ext4, uses remaining space)
parted -s "$OUTPUT_IMG" mkpart primary ext4 $((BOOT_SIZE_MB + ROOT_SIZE_MB * 2))MiB 100%

echo ""
echo "Partition layout:"
parted -s "$OUTPUT_IMG" print

echo ""
echo "Setting up loop device for output image..."

# Set up loop device for output
LOOP_DEV=$(losetup -f --show "$OUTPUT_IMG")
echo "  Loop device: $LOOP_DEV"

# Create device mappings
kpartx -av "$LOOP_DEV"
sleep 2

# Get mapper device names
MAPPER_BASE=$(basename "$LOOP_DEV")
BOOT_PART="/dev/mapper/${MAPPER_BASE}p1"
ROOT_A_PART="/dev/mapper/${MAPPER_BASE}p2"
ROOT_B_PART="/dev/mapper/${MAPPER_BASE}p3"
DATA_PART="/dev/mapper/${MAPPER_BASE}p4"

# Wait for devices to appear
echo "Waiting for partition devices..."
for i in {1..10}; do
    if [ -e "$BOOT_PART" ] && [ -e "$ROOT_A_PART" ] && [ -e "$ROOT_B_PART" ] && [ -e "$DATA_PART" ]; then
        break
    fi
    sleep 1
done

if [ ! -e "$BOOT_PART" ]; then
    echo "Error: Partition devices did not appear"
    exit 1
fi

# Format partitions
echo ""
echo "Formatting partitions..."
echo "  Formatting boot partition (FAT32)..."
mkfs.vfat -F 32 -n "boot" "$BOOT_PART"

echo "  Formatting Root A (ext4)..."
mkfs.ext4 -L "rootfs-a" -q "$ROOT_A_PART"

echo "  Formatting Root B (ext4)..."
mkfs.ext4 -L "rootfs-b" -q "$ROOT_B_PART"

echo "  Formatting Data partition (ext4)..."
mkfs.ext4 -L "data" -q "$DATA_PART"

# Copy content from input image if it exists and is a valid image
if file "$INPUT_IMG" | grep -q "boot sector"; then
    echo ""
    echo "Copying content from source image..."

    # Set up loop device for input image
    INPUT_LOOP=$(losetup -f --show -r "$INPUT_IMG")
    kpartx -av "$INPUT_LOOP"
    sleep 2

    INPUT_MAPPER_BASE=$(basename "$INPUT_LOOP")
    INPUT_BOOT="/dev/mapper/${INPUT_MAPPER_BASE}p1"
    INPUT_ROOT="/dev/mapper/${INPUT_MAPPER_BASE}p2"

    # Wait for input devices
    for i in {1..10}; do
        if [ -e "$INPUT_BOOT" ] && [ -e "$INPUT_ROOT" ]; then
            break
        fi
        sleep 1
    done

    # Create mount points
    MOUNT_BOOT_SRC=$(mktemp -d)
    MOUNT_BOOT_DST=$(mktemp -d)
    MOUNT_ROOT_SRC=$(mktemp -d)
    MOUNT_ROOT_A_DST=$(mktemp -d)
    MOUNT_ROOT_B_DST=$(mktemp -d)

    # Mount all partitions
    echo "  Mounting partitions..."
    mount -o ro "$INPUT_BOOT" "$MOUNT_BOOT_SRC"
    mount "$BOOT_PART" "$MOUNT_BOOT_DST"
    mount -o ro "$INPUT_ROOT" "$MOUNT_ROOT_SRC"
    mount "$ROOT_A_PART" "$MOUNT_ROOT_A_DST"
    mount "$ROOT_B_PART" "$MOUNT_ROOT_B_DST"

    # Copy boot partition
    echo "  Copying boot partition..."
    cp -a "$MOUNT_BOOT_SRC"/* "$MOUNT_BOOT_DST"/

    # Copy rootfs to both Root A and Root B
    echo "  Copying rootfs to Root A..."
    cp -a "$MOUNT_ROOT_SRC"/* "$MOUNT_ROOT_A_DST"/

    echo "  Copying rootfs to Root B..."
    cp -a "$MOUNT_ROOT_SRC"/* "$MOUNT_ROOT_B_DST"/

    # Unmount (cleanup will handle this, but do it explicitly for sync)
    sync
    echo "  Unmounting..."
    umount "$MOUNT_ROOT_B_DST"
    umount "$MOUNT_ROOT_A_DST"
    umount "$MOUNT_ROOT_SRC"
    umount "$MOUNT_BOOT_DST"
    umount "$MOUNT_BOOT_SRC"

    # Clean up input image loop device
    kpartx -d "$INPUT_LOOP"
    losetup -d "$INPUT_LOOP"
    INPUT_LOOP=""

    # Clean up mount directories
    rm -rf "$MOUNT_BOOT_SRC" "$MOUNT_BOOT_DST" "$MOUNT_ROOT_SRC" "$MOUNT_ROOT_A_DST" "$MOUNT_ROOT_B_DST"
    MOUNT_BOOT_SRC=""
    MOUNT_BOOT_DST=""
    MOUNT_ROOT_SRC=""
    MOUNT_ROOT_A_DST=""
    MOUNT_ROOT_B_DST=""
fi

# Clean up output image loop device
kpartx -d "$LOOP_DEV"
losetup -d "$LOOP_DEV"
LOOP_DEV=""

echo ""
echo "Image created successfully: $OUTPUT_IMG"
echo ""
echo "To use this image:"
echo "  1. Flash to SD card: sudo dd if=$OUTPUT_IMG of=/dev/sdX bs=4M status=progress"
echo "  2. Data partition will auto-expand on first boot"
