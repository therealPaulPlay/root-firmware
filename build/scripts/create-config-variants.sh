#!/bin/bash
# Create variant images from a base image
#
# Usage: sudo ./create-config-variants.sh <base.img> <version>
#
# Takes a base image with CAMERA_CONFIG_PLACEHOLDER in config.txt and produces:
#   root-observer-<version>-auto-detect.img  (camera_auto_detect=1)
#   root-observer-<version>-custom.img       (dtoverlay=imx290)
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

PLACEHOLDER="CAMERA_CONFIG_PLACEHOLDER"
AUTO_DETECT_CONFIG="camera_auto_detect=1"
CUSTOM_CONFIG="dtoverlay=imx290"
IMG_DIR=$(dirname "$BASE_IMG")
AUTO_DETECT_IMG="${IMG_DIR}/root-observer-${VERSION}-auto-detect.img"
CUSTOM_IMG="${IMG_DIR}/root-observer-${VERSION}-custom.img"

# Track resources for cleanup
LOOP_DEV=""
MOUNT_DIR=""

cleanup() {
    local exit_code=$?
    echo "Cleaning up..."
    [ -n "$MOUNT_DIR" ] && mountpoint -q "$MOUNT_DIR" && umount "$MOUNT_DIR" 2>/dev/null || true
    [ -n "$MOUNT_DIR" ] && rm -rf "$MOUNT_DIR" 2>/dev/null || true
    [ -n "$LOOP_DEV" ] && kpartx -d "$LOOP_DEV" 2>/dev/null || true
    [ -n "$LOOP_DEV" ] && losetup -d "$LOOP_DEV" 2>/dev/null || true
    exit $exit_code
}
trap cleanup EXIT

# Patch a single image: replace the placeholder in config.txt on the boot partition
# Arguments: <image_path> <replacement_line>
patch_image() {
    local img="$1"
    local replacement="$2"

    echo "  Setting up loop device..."
    LOOP_DEV=$(losetup -f --show "$img")
    kpartx -av "$LOOP_DEV" > /dev/null
    sleep 1

    # Boot partition is partition 1
    local mapper_base
    mapper_base=$(basename "$LOOP_DEV")
    local boot_part="/dev/mapper/${mapper_base}p1"

    # Wait for device to appear
    for i in {1..10}; do
        [ -e "$boot_part" ] && break
        sleep 1
    done

    if [ ! -e "$boot_part" ]; then
        echo "Error: Boot partition device did not appear: $boot_part"
        exit 1
    fi

    MOUNT_DIR=$(mktemp -d)
    mount "$boot_part" "$MOUNT_DIR"

    local config_file="${MOUNT_DIR}/config.txt"
    if [ ! -f "$config_file" ]; then
        echo "Error: config.txt not found on boot partition"
        exit 1
    fi

    # Verify placeholder exists
    if ! grep -q "$PLACEHOLDER" "$config_file"; then
        echo "Error: Placeholder '$PLACEHOLDER' not found in config.txt"
        exit 1
    fi

    # Replace placeholder
    sed -i "s/${PLACEHOLDER}/${replacement}/" "$config_file"

    # Verify replacement succeeded
    if grep -q "$PLACEHOLDER" "$config_file"; then
        echo "Error: Placeholder replacement failed"
        exit 1
    fi

    echo "  Config set to: $replacement"

    # Clean up loop device and mount for this image
    sync
    umount "$MOUNT_DIR"
    rm -rf "$MOUNT_DIR"
    MOUNT_DIR=""
    kpartx -d "$LOOP_DEV" > /dev/null
    losetup -d "$LOOP_DEV"
    LOOP_DEV=""
}

echo "Creating camera variant images..."
echo "  Base image: $BASE_IMG"
echo "  Auto-detect: $AUTO_DETECT_IMG"
echo "  Custom:      $CUSTOM_IMG"
echo ""

# Rename base to first variant, copy for second
echo "Renaming base image to auto-detect variant..."
mv "$BASE_IMG" "$AUTO_DETECT_IMG"

echo "Copying auto-detect image for custom variant..."
cp "$AUTO_DETECT_IMG" "$CUSTOM_IMG"

# Patch each variant
echo ""
echo "Patching auto-detect variant..."
patch_image "$AUTO_DETECT_IMG" "$AUTO_DETECT_CONFIG"

echo ""
echo "Patching custom variant..."
patch_image "$CUSTOM_IMG" "$CUSTOM_CONFIG"

echo ""
echo "Camera variants created successfully:"
echo "  $AUTO_DETECT_IMG"
echo "  $CUSTOM_IMG"
