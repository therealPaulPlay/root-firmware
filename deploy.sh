#!/bin/bash
set -e

PI_HOST="${1:-observer@ROOT-Observer.local}"
REPO="/home/observer/firmware-repository"
VERSION="${FIRMWARE_VERSION:-dev}"

echo "Deploying source..."
ssh "$PI_HOST" "mkdir -p $REPO"
rsync -az --delete --progress --exclude 'root-firmware' --exclude '*.swp' ./ "$PI_HOST:$REPO/"

echo "Building on Pi..."
ssh "$PI_HOST" "cd $REPO && go build -v -ldflags \"-X 'pkg/globals.FirmwareVersion=$VERSION'\" -o root-firmware cmd/main.go"

echo "Installing service..."
ssh "$PI_HOST" "sudo systemctl stop root-firmware && sudo mkdir -p /opt/root-firmware /data && sudo cp $REPO/root-firmware /opt/root-firmware/root-firmware && sudo cp $REPO/build/pi-gen/stage-root-observer/01-system-config/files/systemd/root-firmware.service /etc/systemd/system/ && sudo systemctl daemon-reload && sudo systemctl enable root-firmware && nohup sudo systemctl start root-firmware > /dev/null 2>&1 &"

echo "Done"
