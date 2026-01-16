#!/bin/bash -e
# Copy rootfs from previous stage (stage2)
if [ ! -d "${PREV_ROOTFS_DIR}" ]; then
    echo "Previous stage rootfs not found at ${PREV_ROOTFS_DIR}"
    exit 1
fi

# Create the rootfs directory for this stage
mkdir -p "${ROOTFS_DIR}"

rsync -aHAXx --exclude var/cache/apt/archives "${PREV_ROOTFS_DIR}/" "${ROOTFS_DIR}/"
