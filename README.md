# root-firmware

Firmware for ROOT camera devices. Built in Go with a focus on privacy, security, and observability.

## Building

To build the firmware with a specific version:

```bash
go build -ldflags="-X 'root-firmware/pkg/globals.FirmwareVersion=1.0.0'" -o root-firmware cmd/main.go
```

The version is injected at build time via the `-ldflags` flag. If you build without specifying a version, it defaults to `dev`.

## Deploying to Pi Zero 2

Prerequisites: Create user `observer`, set hostname to `ROOT-Observer`, install ONNX Runtime on the Pi.

Deploy:

```bash
./deploy.sh [user@hostname]
```

This cross-compiles on your Mac, syncs the binary to `/home/observer/root-firmware`, and auto-starts via systemd.

Check if running:

```bash
ssh observer@ROOT-Observer.local 'pgrep -f root-firmware'
```

## Compiling the ONNX runtime for the Pi

Since the camera hardware is slow compared to modern computers, compiling inside a docker container on a fast machine is recommended over compiling on the single-board computer itself. Use this script to spin up a docker container & compile the runtime. Ensure `docker` is installed on your system.

```bash
docker run -it --rm --platform linux/arm64 \
  -v $(pwd)/onnx-output:/output \
  ubuntu:22.04 bash -c '
apt update && \
apt install -y cmake build-essential pkg-config git python3 python3-pip && \
pip3 install setuptools && \
git clone --depth 1 -b v1.9.1 https://github.com/microsoft/onnxruntime && \
cd onnxruntime && \
git submodule update --init --recursive && \
./build.sh --config Release \
  --build_shared_lib \
  --parallel 4 \
  --skip_tests \
  --skip_submodule_sync \
  --disable_rtti \
  --cmake_extra_defines CMAKE_CXX_FLAGS="-w" && \
cp build/Linux/Release/libonnxruntime.so* /output/ && \
echo "Build complete! Files in ./onnx-output/"
'
```

## Package overview

### Config

All configuration values are stored in a config `JSON` file in the `/data`/ partition.

### Devices

The `devices` package is for managing paired devices.

### Encryption

This package exposes functions for creating encryption keys using the Diffie-Hellman key exchange method.

### Globals

Global variables. All paths or other constants that are reused across packages go here.

### Logger

Collect logs and store them in a JSON for easy access.

### ML (Machine learning)

Uses ONNX for basic event detection. Heavily inspired by [Secluso](https://github.com/secluso/secluso). 

### Pairing

The firmware creates a WiFi access point and exposes a couple of endpoints needed for essential configurations. 

### Record

Handles recording video and audio via the camera and microphone components.

### Relaycomm

Communication with the device the firmware runs on happens via a relay. In the `relaycomm` package, the WebSocket connection is being handled.

### Speaker

Play sound via the speaker component and stream audio for two-way-audio.

### Storage

Save recordings and update the event log.

### Updater

Check for and download firmware updates.

### UPS

Obtain data from the uninterruptible power supply (battery percentage etc.). Built for the [Waveshare](https://www.waveshare.com/ups-hat-c.htm) Raspberry Pi Zero 2 UPS. 

### WiFi

Scan for WiFi networks and establish a wifi connection.