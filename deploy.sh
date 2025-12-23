#!/bin/bash
set -e

PI_HOST="${1:-observer@ROOT-Observer.local}"
VERSION="${FIRMWARE_VERSION:-dev}"

echo "Checking prerequisites..."
ssh "$PI_HOST" "ldconfig -p | grep -q onnxruntime || exit 1"

echo "Building..."
GOOS=linux GOARCH=arm GOARM=7 go build -ldflags "-X 'pkg/globals.FirmwareVersion=$VERSION'" -o root-firmware cmd/main.go

echo "Deploying..."
rsync -az --progress root-firmware root-firmware.service "$PI_HOST:/home/observer/"

echo "Installing service..."
ssh "$PI_HOST" "sudo cp /home/observer/root-firmware.service /etc/systemd/system/ && sudo systemctl daemon-reload && sudo systemctl enable --now root-firmware"

echo "Done"
