#!/bin/bash -e
# ROOT Observer system configuration

# Enable I2C
on_chroot << EOF
raspi-config nonint do_i2c 0
EOF

# Install ONNX runtime libraries
install -d "${ROOTFS_DIR}/usr/local/lib"
install -m 755 "../../onnx-precompiled/libonnxruntime.so.1.23.2" "${ROOTFS_DIR}/usr/local/lib/"
ln -sf libonnxruntime.so.1.23.2 "${ROOTFS_DIR}/usr/local/lib/libonnxruntime.so.1"
ln -sf libonnxruntime.so.1 "${ROOTFS_DIR}/usr/local/lib/libonnxruntime.so"

# Configure dynamic linker to find libraries in /usr/local/lib
echo "/usr/local/lib" > "${ROOTFS_DIR}/etc/ld.so.conf.d/local.conf"
on_chroot << EOF
ldconfig
EOF

# Copy RAUC system configuration
install -d "${ROOTFS_DIR}/etc/rauc"
install -m 644 files/rauc/system.conf "${ROOTFS_DIR}/etc/rauc/system.conf"

# Install RAUC verification certificate (will be added during build)
if [ -f "${STAGE_WORK_DIR}/rauc-ca.cert.pem" ]; then
    install -m 644 "${STAGE_WORK_DIR}/rauc-ca.cert.pem" "${ROOTFS_DIR}/etc/rauc/ca.cert.pem"
fi

# Install RAUC custom bootloader backend
install -d "${ROOTFS_DIR}/usr/lib/rauc/backend"
install -m 755 "../../rauc/raspberrypi-firmware-backend" "${ROOTFS_DIR}/usr/lib/rauc/backend/raspberrypi-firmware"

# Copy boot configuration files
install -m 644 files/boot/config.txt "${ROOTFS_DIR}/boot/firmware/config.txt"
install -m 644 files/boot/cmdline.txt "${ROOTFS_DIR}/boot/firmware/cmdline.txt"

# Install systemd services
install -m 644 files/systemd/root-firmware.service "${ROOTFS_DIR}/etc/systemd/system/"
install -m 644 files/systemd/data-partition-expand.service "${ROOTFS_DIR}/etc/systemd/system/"

# Install data partition expansion script
install -d "${ROOTFS_DIR}/usr/lib/root-firmware"
install -m 755 files/systemd/expand-data-partition.sh "${ROOTFS_DIR}/usr/lib/root-firmware/"

# Enable services
on_chroot << EOF
systemctl enable root-firmware.service
systemctl enable data-partition-expand.service
EOF

# Create observer user
on_chroot << EOF
if ! id -u observer &>/dev/null; then
    useradd -m -s /bin/bash observer
fi
EOF

# Create data directory mount point
install -d "${ROOTFS_DIR}/data"

# Add fstab entry for data partition (partition 4)
cat >> "${ROOTFS_DIR}/etc/fstab" << 'FSTAB'
/dev/mmcblk0p4  /data  ext4  defaults,noatime,data=ordered  0  2
FSTAB

# Set hostname
echo "ROOT-Observer" > "${ROOTFS_DIR}/etc/hostname"

# Configure hosts file
cat > "${ROOTFS_DIR}/etc/hosts" << 'HOSTS'
127.0.0.1       localhost
127.0.1.1       ROOT-Observer

::1             localhost ip6-localhost ip6-loopback
ff02::1         ip6-allnodes
ff02::2         ip6-allrouters
HOSTS
