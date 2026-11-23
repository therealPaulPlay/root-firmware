#!/bin/bash
# Repartition a pi-gen image for the A/B boot + root partition layout
#
# Usage: sudo ./repartition-image.sh <input.img> <output.img>
#
# Creates a 7-partition layout (MBR):
#   1. Bootloader (512MiB, FAT32)
#   2. Boot A (512MiB, FAT32)
#   3. Boot B (512MiB, FAT32)
#   4. Extended partition containing:
#      5. Root A (5GiB, ext4)
#      6. Root B (5GiB, ext4)
#      7. Data (remaining space, ext4)
#
# Prerequisites: parted, losetup, kpartx, dosfstools, e2fsprogs
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

if ! file "$INPUT_IMG" | grep -q "boot sector"; then
    echo "Error: Input is not a disk image: $INPUT_IMG"
    exit 1
fi

if [ "$(id -u)" -ne 0 ]; then
    echo "Error: This script requires root privileges (loop devices)"
    echo "Usage: sudo $0 <input.img> <output.img>"
    exit 1
fi

# Sizes in MiB
# Boot and root sizes are fixed on deployed devices
BOOTLOADER_SIZE_MB=512
BOOT_SIZE_MB=512
ROOT_SIZE_MB=5120
DATA_MIN_SIZE_MB=5120
# Each logical partition is preceded by its EBR
EBR_GAP_MB=1

# Partition boundaries in MiB
P1_START=1
P2_START=$((P1_START + BOOTLOADER_SIZE_MB))
P3_START=$((P2_START + BOOT_SIZE_MB))
EXT_START=$((P3_START + BOOT_SIZE_MB))
P5_START=$((EXT_START + EBR_GAP_MB))
P5_END=$((P5_START + ROOT_SIZE_MB))
P6_START=$((P5_END + EBR_GAP_MB))
P6_END=$((P6_START + ROOT_SIZE_MB))
P7_START=$((P6_END + EBR_GAP_MB))
TOTAL_SIZE_MB=$((P7_START + DATA_MIN_SIZE_MB))

echo "Creating A/B partition image..."
echo "  Bootloader: ${BOOTLOADER_SIZE_MB}MiB"
echo "  Boot A/B:   ${BOOT_SIZE_MB}MiB each"
echo "  Root A/B:   ${ROOT_SIZE_MB}MiB each"
echo "  Data:       ${DATA_MIN_SIZE_MB}MiB+ (expands on first boot)"
echo "  Total:      ${TOTAL_SIZE_MB}MiB minimum"

# Track resources for cleanup
LOOP_DEV=""
INPUT_LOOP=""
MOUNTS=()

cleanup() {
    local exit_code=$?
    local dir
    echo "Cleaning up..."
    # Unmount any mounted filesystems (|| true ensures cleanup never fails the script)
    for dir in "${MOUNTS[@]}"; do
        mountpoint -q "$dir" && umount "$dir" 2>/dev/null || true
        # rmdir keeps files if still mounted
        rmdir "$dir" 2>/dev/null || true
    done

    # Clean up loop devices
    [ -n "$INPUT_LOOP" ] && kpartx -d "$INPUT_LOOP" 2>/dev/null || true
    [ -n "$INPUT_LOOP" ] && losetup -d "$INPUT_LOOP" 2>/dev/null || true
    [ -n "$LOOP_DEV" ] && kpartx -d "$LOOP_DEV" 2>/dev/null || true
    [ -n "$LOOP_DEV" ] && losetup -d "$LOOP_DEV" 2>/dev/null || true

    exit $exit_code
}
trap cleanup EXIT

all_exist() {
    local dev
    for dev in "$@"; do
        [ -e "$dev" ] || return 1
    done
}

# Wait for devices to appear
wait_for_partitions() {
    for _ in {1..10}; do
        all_exist "$@" && return 0
        sleep 1
    done
    echo "Error: Partition devices did not appear: $*"
    exit 1
}

# Create output image with required size
echo ""
echo "Creating image file..."
truncate -s ${TOTAL_SIZE_MB}M "$OUTPUT_IMG"

# Create partition table
echo "Creating partition table..."
parted -s "$OUTPUT_IMG" mklabel msdos

# Partition 1: Bootloader (FAT32)
parted -s "$OUTPUT_IMG" mkpart primary fat32 ${P1_START}MiB ${P2_START}MiB
parted -s "$OUTPUT_IMG" set 1 boot on

# Partition 2 and 3: Boot A and B (FAT32)
parted -s "$OUTPUT_IMG" mkpart primary fat32 ${P2_START}MiB ${P3_START}MiB
parted -s "$OUTPUT_IMG" mkpart primary fat32 ${P3_START}MiB ${EXT_START}MiB

# Partition 4: Extended (uses remaining space)
parted -s "$OUTPUT_IMG" mkpart extended ${EXT_START}MiB 100%

# Partition 5 and 6: Root A and B (ext4)
parted -s "$OUTPUT_IMG" mkpart logical ext4 ${P5_START}MiB ${P5_END}MiB
parted -s "$OUTPUT_IMG" mkpart logical ext4 ${P6_START}MiB ${P6_END}MiB

# Partition 7: Data (ext4, uses remaining space)
parted -s "$OUTPUT_IMG" mkpart logical ext4 ${P7_START}MiB 100%

echo ""
echo "Partition layout:"
parted -s "$OUTPUT_IMG" print

# Set up loop devices
echo ""
echo "Setting up loop devices..."
LOOP_DEV=$(losetup -f --show "$OUTPUT_IMG")
kpartx -av "$LOOP_DEV"
OUT="/dev/mapper/$(basename "$LOOP_DEV")"
BOOTLOADER_PART="${OUT}p1"
BOOT_A_PART="${OUT}p2"
BOOT_B_PART="${OUT}p3"
ROOT_A_PART="${OUT}p5"
ROOT_B_PART="${OUT}p6"
DATA_PART="${OUT}p7"
wait_for_partitions "$BOOTLOADER_PART" "$BOOT_A_PART" "$BOOT_B_PART" "$ROOT_A_PART" "$ROOT_B_PART" "$DATA_PART"

INPUT_LOOP=$(losetup -f --show -r "$INPUT_IMG")
kpartx -av "$INPUT_LOOP"
IN="/dev/mapper/$(basename "$INPUT_LOOP")"
wait_for_partitions "${IN}p1" "${IN}p2"

# Format partitions
echo ""
echo "Formatting partitions..."
mkfs.vfat -F 32 -n "BOOTLOADER" "$BOOTLOADER_PART"
# Same labels, updates copy A to B
mkfs.vfat -F 32 -n "boot" "$BOOT_A_PART"
mkfs.vfat -F 32 -n "boot" "$BOOT_B_PART"
mkfs.ext4 -L "rootfs" -q "$ROOT_A_PART"
mkfs.ext4 -L "rootfs" -q "$ROOT_B_PART"
mkfs.ext4 -L "data" -q "$DATA_PART"

# Create mount points
SRC_BOOT=$(mktemp -d)
SRC_ROOT=$(mktemp -d)
DST_BOOTLOADER=$(mktemp -d)
DST_BOOT_A=$(mktemp -d)
DST_BOOT_B=$(mktemp -d)
DST_ROOT_A=$(mktemp -d)
DST_ROOT_B=$(mktemp -d)
MOUNTS=("$SRC_BOOT" "$SRC_ROOT" "$DST_BOOTLOADER" "$DST_BOOT_A" "$DST_BOOT_B" "$DST_ROOT_A" "$DST_ROOT_B")

# Mount all partitions
echo ""
echo "Mounting partitions..."
mount -o ro "${IN}p1" "$SRC_BOOT"
mount -o ro "${IN}p2" "$SRC_ROOT"
mount "$BOOTLOADER_PART" "$DST_BOOTLOADER"
mount "$BOOT_A_PART" "$DST_BOOT_A"
mount "$BOOT_B_PART" "$DST_BOOT_B"
mount "$ROOT_A_PART" "$DST_ROOT_A"
mount "$ROOT_B_PART" "$DST_ROOT_B"

# Firmware needed to read autoboot.txt
echo "  Populating bootloader partition..."
cp "$SRC_BOOT"/{bootcode.bin,start.elf,fixup.dat} "$DST_BOOTLOADER"/

# Start on Boot A
cat > "$DST_BOOTLOADER/autoboot.txt" << 'AUTOBOOT'
[all]
tryboot_a_b=1
boot_partition=2
[tryboot]
boot_partition=2
AUTOBOOT

# Copy boot partition to both Boot A and Boot B
echo "  Copying boot files to Boot A and Boot B..."
for dst in "$DST_BOOT_A" "$DST_BOOT_B"; do
    cp -a "$SRC_BOOT"/* "$dst"/
    # Remove pi-gen's default cmdline.txt, only cmdline_a.txt and cmdline_b.txt are used
    rm -f "$dst/cmdline.txt"
done

# Copy rootfs to both Root A and Root B
echo "  Copying rootfs to Root A and Root B..."
for dst in "$DST_ROOT_A" "$DST_ROOT_B"; do
    cp -a "$SRC_ROOT"/* "$dst"/
done

# Unmount explicitly so failures fail the build
sync
echo "  Unmounting..."
umount "${MOUNTS[@]}"

echo ""
echo "Image created successfully: $OUTPUT_IMG"
echo ""
echo "To use this image:"
echo "  1. Flash to SD card: sudo dd if=$OUTPUT_IMG of=/dev/sdX bs=4M status=progress"
echo "  2. Data partition will auto-expand on first boot"
