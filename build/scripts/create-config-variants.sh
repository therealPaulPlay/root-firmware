#!/bin/bash
# Create hardware variant images from a base image
#
# Usage: sudo ./create-config-variants.sh <base.img> <version>
#
# Produces one image per variant:
#   root-observer-<version>-auto-detect.img  (any officially supported Pi camera module)
#   root-observer-<version>-custom.img       (ROOT hardware)
#
# Prerequisites: losetup, kpartx, mount (FAT32 support)
# Note: Requires sudo/root for loop device operations

set -e

BASE_IMG="$1"
VERSION="$2"

if [ -z "$BASE_IMG" ] || [ -z "$VERSION" ]; then
    echo "Usage: sudo $0 <base.img> <version>"
    exit 1
fi

if [ ! -f "$BASE_IMG" ]; then
    echo "Error: Base image not found: $BASE_IMG"
    exit 1
fi

if [ "$(id -u)" -ne 0 ]; then
    echo "Error: This script requires root privileges (loop devices)"
    echo "Usage: sudo $0 <base.img> <version>"
    exit 1
fi

VARIANTS=(auto-detect custom)
LAST_VARIANT="${VARIANTS[-1]}"
BOOTLOADER_PART=1
BOOT_PARTS=(2 3)
IMG_DIR=$(dirname "$BASE_IMG")

# Track resources for cleanup
LOOP_DEV=""
MOUNT_DIR=""

cleanup() {
    local exit_code=$?
    echo "Cleaning up..."
    [ -n "$MOUNT_DIR" ] && mountpoint -q "$MOUNT_DIR" && umount "$MOUNT_DIR" 2>/dev/null || true
    [ -n "$MOUNT_DIR" ] && rmdir "$MOUNT_DIR" 2>/dev/null || true
    [ -n "$LOOP_DEV" ] && kpartx -d "$LOOP_DEV" 2>/dev/null || true
    [ -n "$LOOP_DEV" ] && losetup -d "$LOOP_DEV" 2>/dev/null || true
    exit $exit_code
}
trap cleanup EXIT

# Mount a partition of the image on MOUNT_DIR
# Arguments: <partition_number>
mount_part() {
    local dev="/dev/mapper/$(basename "$LOOP_DEV")p$1"

    # Wait for device to appear
    for _ in {1..10}; do
        [ -e "$dev" ] && break
        sleep 1
    done

    if [ ! -e "$dev" ]; then
        echo "Error: Partition device did not appear: $dev"
        exit 1
    fi

    MOUNT_DIR=$(mktemp -d)
    mount "$dev" "$MOUNT_DIR"
}

unmount_part() {
    sync
    umount "$MOUNT_DIR"
    rmdir "$MOUNT_DIR"
    MOUNT_DIR=""
}

# Store the variant name for the RAUC hook
write_variant_name() {
    local variant="$1"

    mount_part "$BOOTLOADER_PART"
    echo "$variant" > "$MOUNT_DIR/variant"
    echo "  p${BOOTLOADER_PART}: variant = $variant"
    unmount_part
}

# Copy the variant config to variant.txt
# Arguments: <partition_number> <variant>
select_variant_config() {
    local part="$1"
    local variant="$2"

    mount_part "$part"

    if ! grep -qx "include variant.txt" "$MOUNT_DIR/config.txt" 2>/dev/null; then
        echo "Error: config.txt on p${part} is missing or doesn't include variant.txt"
        exit 1
    fi

    if [ ! -f "$MOUNT_DIR/variants/${variant}.txt" ]; then
        echo "Error: variants/${variant}.txt not found on p${part}"
        exit 1
    fi

    cp "$MOUNT_DIR/variants/${variant}.txt" "$MOUNT_DIR/variant.txt"
    echo "  p${part}: variant.txt = variants/${variant}.txt"
    unmount_part
}

# Patch a single image for a variant
# Arguments: <image_path> <variant>
patch_image() {
    local img="$1"
    local variant="$2"

    echo "  Setting up loop device..."
    LOOP_DEV=$(losetup -f --show "$img")
    kpartx -av "$LOOP_DEV" > /dev/null

    write_variant_name "$variant"
    for part in "${BOOT_PARTS[@]}"; do
        select_variant_config "$part" "$variant"
    done

    # Clean up loop device for this image
    kpartx -d "$LOOP_DEV" > /dev/null
    losetup -d "$LOOP_DEV"
    LOOP_DEV=""
}

echo "Creating variant images from $BASE_IMG..."

for variant in "${VARIANTS[@]}"; do
    img="${IMG_DIR}/root-observer-${VERSION}-${variant}.img"
    echo ""
    echo "Creating $variant variant: $img"

    # Last variant reuses the base image
    if [ "$variant" = "$LAST_VARIANT" ]; then
        mv "$BASE_IMG" "$img"
    else
        cp "$BASE_IMG" "$img"
    fi

    patch_image "$img" "$variant"
done

echo ""
echo "Variant images created successfully"
