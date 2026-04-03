#!/bin/bash -e
# ROOT Observer system configuration
# Note: I2C is enabled via dtparam=i2c_arm=on in config.txt

# Install precompiled libraries (copied to stage dir by CI)
install -d "${ROOTFS_DIR}/usr/local/lib"
install -m 755 "${STAGE_DIR}/libonnxruntime.so.1.23.2" "${ROOTFS_DIR}/usr/local/lib/"
ln -sf libonnxruntime.so.1.23.2 "${ROOTFS_DIR}/usr/local/lib/libonnxruntime.so.1"
ln -sf libonnxruntime.so.1 "${ROOTFS_DIR}/usr/local/lib/libonnxruntime.so"
ln -sf libonnxruntime.so "${ROOTFS_DIR}/usr/local/lib/onnxruntime.so"
install -m 755 "${STAGE_DIR}/libopenh264-2.5.1-linux-arm64.7.so" "${ROOTFS_DIR}/usr/local/lib/"
ln -sf libopenh264-2.5.1-linux-arm64.7.so "${ROOTFS_DIR}/usr/local/lib/libopenh264.so"

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
install -m 644 files/boot/cmdline_a.txt "${ROOTFS_DIR}/boot/firmware/cmdline_a.txt"
install -m 644 files/boot/cmdline_b.txt "${ROOTFS_DIR}/boot/firmware/cmdline_b.txt"
# Note:the default cmdline.txt is left untouched for pi-gen's export-image stage (otherwise fails)
# At runtime it's unused because config.txt explicitly sets cmdline=cmdline_a.txt

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
systemctl disable apt-daily.timer || true
systemctl disable apt-daily-upgrade.timer || true
systemctl mask systemd-growfs-root.service || true
systemctl mask rpi-resize.service || true
systemctl mask rpi-resize-swap-file.service || true
EOF

# Configure NetworkManager to have WiFi radio enabled by default
install -d "${ROOTFS_DIR}/var/lib/NetworkManager"
cat > "${ROOTFS_DIR}/var/lib/NetworkManager/NetworkManager.state" << 'NMSTATE'
[main]
NetworkingEnabled=true
WirelessEnabled=true
WWANEnabled=true
NMSTATE

# Disable WiFi power saving (brcmfmac driver can drop connections with it enabled)
install -d "${ROOTFS_DIR}/etc/NetworkManager/conf.d"
cat > "${ROOTFS_DIR}/etc/NetworkManager/conf.d/wifi-powersave-off.conf" << 'NMPOWERSAVE'
[connection]
wifi.powersave = 2
NMPOWERSAVE

# Disable brcmfmac roaming to work around driver crashes during sustained transfers
install -d "${ROOTFS_DIR}/etc/modprobe.d"
cat > "${ROOTFS_DIR}/etc/modprobe.d/brcmfmac.conf" << 'BRCMFMAC'
options brcmfmac roamoff=1
BRCMFMAC

# Disable cloud-init
install -d "${ROOTFS_DIR}/etc/cloud"
touch "${ROOTFS_DIR}/etc/cloud/cloud-init.disabled"

# Configure hardware watchdog — systemd pets it, reboot on 15s timeout
install -d "${ROOTFS_DIR}/etc/systemd/system.conf.d"
cat > "${ROOTFS_DIR}/etc/systemd/system.conf.d/watchdog.conf" << 'WATCHDOG'
[Manager]
RuntimeWatchdogSec=15
WATCHDOG

# Configure journald for volatile storage
install -d "${ROOTFS_DIR}/etc/systemd/journald.conf.d"
cat > "${ROOTFS_DIR}/etc/systemd/journald.conf.d/volatile.conf" << 'JOURNALD'
[Journal]
Storage=volatile
RuntimeMaxUse=16M
JOURNALD

# Create data directory mount point
install -d "${ROOTFS_DIR}/data"

# Persist timesync clock on /data so it survives A/B slot switches
# Uses bind mount because symlinks get overridden by systemd's StateDirectory
install -d "${ROOTFS_DIR}/data/timesync"
on_chroot << EOF
chown systemd-timesync:systemd-timesync /data/timesync
EOF
install -d "${ROOTFS_DIR}/var/lib/systemd/timesync"
cat > "${ROOTFS_DIR}/etc/tmpfiles.d/timesync-persist.conf" << 'TMPFILES'
d /data/timesync 0755 systemd-timesync systemd-timesync -
TMPFILES

# Persist NM connection profiles on /data so WiFi reconnects before firmware starts
install -d "${ROOTFS_DIR}/data/NetworkManager/system-connections"

# Replace fstab — read-only rootfs with overlayfs on /etc and /var/lib,
# persistent bind mounts from /data for NM connections, machine-id, and timesync
cat > "${ROOTFS_DIR}/etc/fstab" << 'FSTAB'
/dev/mmcblk0p1  /boot/firmware  vfat       defaults                             0  2
/dev/mmcblk0p2  /               ext4       ro,noatime                           0  1
/dev/mmcblk0p4  /data           ext4       defaults,noatime                     0  2
tmpfs           /tmp            tmpfs      nosuid,nodev,size=32M                0  0
tmpfs           /var/tmp        tmpfs      nosuid,nodev,size=16M                0  0
tmpfs           /var/log        tmpfs      nosuid,nodev,size=16M                0  0
tmpfs           /var/cache      tmpfs      nosuid,nodev,size=32M                0  0
tmpfs           /mnt            tmpfs      nosuid,nodev,size=16M                0  0
overlay         /var/lib        overlay    lowerdir=/var/lib,upperdir=/run/var-lib-upper,workdir=/run/var-lib-work,x-systemd.requires=early-boot-setup.service  0  0
overlay         /etc            overlay    lowerdir=/etc,upperdir=/run/etc-upper,workdir=/run/etc-work,x-systemd.requires=early-boot-setup.service  0  0
/data/timesync  /var/lib/systemd/timesync  none  bind,nofail,x-systemd.requires=data.mount,x-systemd.requires-mounts-for=/var/lib  0  0
/data/NetworkManager/system-connections  /etc/NetworkManager/system-connections  none  bind,x-systemd.requires=data.mount,x-systemd.requires-mounts-for=/etc  0  0
/data/machine-id  /etc/machine-id  none  bind,nofail,x-systemd.requires=data.mount,x-systemd.requires-mounts-for=/etc  0  0
FSTAB

# Early boot setup: create overlay dirs and initialize machine-id
# Runs before local-fs.target to avoid circular dependency with systemd-tmpfiles-setup
# On first boot, generates a unique machine-id on /data and bind-mounts it;
# on subsequent boots the fstab bind mount handles machine-id
cat > "${ROOTFS_DIR}/etc/systemd/system/early-boot-setup.service" << 'UNIT'
[Unit]
Description=Early boot setup (overlay dirs, machine-id)
DefaultDependencies=no
After=data.mount
Before=local-fs.target

[Service]
Type=oneshot
ExecStart=/bin/mkdir -p /run/var-lib-upper /run/var-lib-work /run/etc-upper /run/etc-work
ExecStart=/bin/mkdir -p /data/NetworkManager/system-connections /data/timesync
ExecStart=/bin/sh -c '[ -f /data/machine-id ] || { cat /proc/sys/kernel/random/uuid | tr -d "-" > /data/machine-id && mount --bind /data/machine-id /etc/machine-id; }'

[Install]
WantedBy=local-fs.target
UNIT

on_chroot << EOF
systemctl enable early-boot-setup.service
EOF

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

