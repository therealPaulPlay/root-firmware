#!/bin/bash -e
# Install required packages for ROOT Observer

on_chroot << EOF
apt-get update
apt-get install -y \
    ffmpeg \
    rpicam-apps \
    rauc \
    i2c-tools \
    alsa-utils \
    bluez \
    parted \
    e2fsprogs
apt-get clean
EOF
