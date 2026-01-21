#!/bin/bash -e
# ROOT Observer system configuration
# Note: I2C is enabled via dtparam=i2c_arm=on in config.txt

# Install ONNX runtime libraries (copied to stage dir by CI)
install -d "${ROOTFS_DIR}/usr/local/lib"
install -m 755 "${STAGE_DIR}/libonnxruntime.so.1.23.2" "${ROOTFS_DIR}/usr/local/lib/"
ln -sf libonnxruntime.so.1.23.2 "${ROOTFS_DIR}/usr/local/lib/libonnxruntime.so.1"
ln -sf libonnxruntime.so.1 "${ROOTFS_DIR}/usr/local/lib/libonnxruntime.so"
ln -sf libonnxruntime.so "${ROOTFS_DIR}/usr/local/lib/onnxruntime.so"

# Configure dynamic linker to find libraries in /usr/local/lib
echo "/usr/local/lib" > "${ROOTFS_DIR}/etc/ld.so.conf.d/local.conf"
on_chroot << EOF
ldconfig
EOF

# Copy RAUC system configuration
install -d "${ROOTFS_DIR}/etc/rauc"
install -m 644 files/rauc/system.conf "${ROOTFS_DIR}/etc/rauc/system.conf"

# Install RAUC D-Bus policy (missing from Debian package)
install -m 644 files/dbus/de.pengutronix.rauc.conf "${ROOTFS_DIR}/usr/share/dbus-1/system.d/"

# Install RAUC verification certificate (copied to stage dir by CI)
if [ -f "${STAGE_DIR}/rauc-ca.cert.pem" ]; then
    install -m 644 "${STAGE_DIR}/rauc-ca.cert.pem" "${ROOTFS_DIR}/etc/rauc/ca.cert.pem"
fi

# Install RAUC custom bootloader backend (copied to stage dir by CI)
install -d "${ROOTFS_DIR}/usr/lib/rauc/backend"
install -m 755 "${STAGE_DIR}/raspberrypi-firmware-backend" "${ROOTFS_DIR}/usr/lib/rauc/backend/raspberrypi-firmware"

# Copy boot configuration files
install -m 644 files/boot/config.txt "${ROOTFS_DIR}/boot/firmware/config.txt"
install -m 644 files/boot/cmdline.txt "${ROOTFS_DIR}/boot/firmware/cmdline.txt"

# Install systemd services
install -m 644 files/systemd/root-firmware.service "${ROOTFS_DIR}/etc/systemd/system/"
install -m 644 files/systemd/data-partition-expand.service "${ROOTFS_DIR}/etc/systemd/system/"
install -m 644 files/systemd/rauc.service "${ROOTFS_DIR}/etc/systemd/system/"
install -m 644 files/systemd/rauc-boot-watchdog.service "${ROOTFS_DIR}/etc/systemd/system/"

# Install data partition expansion script
install -d "${ROOTFS_DIR}/usr/lib/root-firmware"
install -m 755 files/systemd/expand-data-partition.sh "${ROOTFS_DIR}/usr/lib/root-firmware/"

# Enable services
on_chroot << EOF
systemctl enable root-firmware.service
systemctl enable data-partition-expand.service
systemctl enable rauc.service
systemctl enable rauc-boot-watchdog.service
EOF

# Disable unwanted services
on_chroot << EOF
systemctl disable dphys-swapfile.service || true
systemctl mask dphys-swapfile.service || true
systemctl disable avahi-daemon.service || true
systemctl disable avahi-daemon.socket || true
systemctl mask avahi-daemon.service || true
systemctl mask avahi-daemon.socket || true
EOF

# Configure NetworkManager to have WiFi radio enabled by default
install -d "${ROOTFS_DIR}/var/lib/NetworkManager"
cat > "${ROOTFS_DIR}/var/lib/NetworkManager/NetworkManager.state" << 'NMSTATE'
[main]
NetworkingEnabled=true
WirelessEnabled=true
WWANEnabled=true
NMSTATE

# Disable cloud-init
install -d "${ROOTFS_DIR}/etc/cloud"
touch "${ROOTFS_DIR}/etc/cloud/cloud-init.disabled"

# Configure journald for volatile storage
install -d "${ROOTFS_DIR}/etc/systemd/journald.conf.d"
cat > "${ROOTFS_DIR}/etc/systemd/journald.conf.d/volatile.conf" << 'JOURNALD'
[Journal]
Storage=volatile
RuntimeMaxUse=16M
JOURNALD

# Create data directory mount point
install -d "${ROOTFS_DIR}/data"

# Replace fstab with our custom partition layout
cat > "${ROOTFS_DIR}/etc/fstab" << 'FSTAB'
/dev/mmcblk0p1  /boot/firmware  vfat    defaults            0  2
/dev/mmcblk0p2  /               ext4    defaults,noatime    0  1
/dev/mmcblk0p4  /data           ext4    defaults,noatime    0  2
tmpfs           /tmp            tmpfs   nosuid,nodev        0  0
tmpfs           /var/tmp        tmpfs   nosuid,nodev        0  0
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

