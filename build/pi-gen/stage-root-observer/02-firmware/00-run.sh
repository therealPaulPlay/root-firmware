#!/bin/bash -e
# Install pre-built ROOT Observer firmware binary

# The firmware binary should be placed in STAGE_DIR by the CI build process
FIRMWARE_BINARY="${STAGE_DIR}/root-firmware"

if [ -f "$FIRMWARE_BINARY" ]; then
    install -d "${ROOTFS_DIR}/home/observer"
    install -m 755 "$FIRMWARE_BINARY" "${ROOTFS_DIR}/home/observer/root-firmware"

    # Set ownership
    on_chroot << EOF
chown observer:observer /home/observer/root-firmware
EOF
else
    echo "Warning: Firmware binary not found at $FIRMWARE_BINARY"
    echo "The firmware will need to be installed separately"
fi
