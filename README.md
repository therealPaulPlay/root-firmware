# ROOT (Firmware)

Firmware for ROOT camera devices. Written in Go with a focus on privacy, security, and observability.

## Features and benefits

This firmware turns a Raspberry Pi Zero 2w+ or Pi 3+ into a private home security camera. It utilizes end-2-end encryption with forward secrecy and connects to the relay server (https://github.com/therealPaulPlay/root-relay)[repository] to communicate with the (https://github.com/therealPaulPlay/root-site)[web and mobile app].

A relay server solely relays the data that's sent across and has no ability to decrypt it. During setup, the relay server URL can be determined, making it easy to choose a self-hosted instance. 

In the web app, you can connect to paired cameras, adjust settings such as enabling/disabling the microphone (if connected) as well as viewing recorded events, logs, and core health metrics. Streaming both video and audio is supported with low latency.

## Contributing

Contributions are welcome. For inquiries, please reach out via (mailto:paulplaystudio@gmail.com)[email] or Bluesky (@paulplay.bsky.social).

## Tested SBCs

- Pi Zero 2w

## Building

To build the firmware with a specific version:

```bash
go build -ldflags="-X 'root-firmware/pkg/globals.FirmwareVersion=1.0.0'" -o root-firmware cmd/main.go
```

The version is injected at build time via the `-ldflags` flag. If you build without specifying a version, it defaults to `dev`.

## Deploying to Raspberry Pi

Prerequisites: Create user `observer`, set hostname to `ROOT-Observer.local`, install Go and ONNX Runtime on the Pi. Raspian OS Bookworm or higher is required.

Deploy:

```bash
./deploy.sh
```

This syncs source to `/home/observer/firmware-repository`, builds on the Pi, copies binary to `/home/observer/root-firmware`, and auto-starts via systemd.

Check if running:

```bash
ssh observer@ROOT-Observer.local 'pgrep -f root-firmware'
```

### Setting up an SSH Key

Using an SSH key for development instead of an SSH password is significantly more convenient. Create one using `ssh-keygen -t ed25519 -C "your_email@example.com"` and copy it onto the Pi using `ssh-copy-id observer@ROOT-Observer.local`. 

## Installing ONNX Runtime on the Pi

Since the camera hardware is slow compared to modern computers, compiling inside a docker container on a fast machine is recommended over compiling on the single-board computer itself.

### 1. Compile ONNX Runtime

Use this script to spin up a docker container & compile the runtime. Ensure `docker` is installed on your system.

```bash
docker run -it --rm --platform linux/arm64 \
  -v "$(pwd)/onnx-output:/output" \
  ubuntu:24.04 bash -c '
apt update && \
apt install -y cmake build-essential pkg-config git python3 python3-pip python3-setuptools python3-wheel && \
git clone --depth 1 -b v1.23.2 https://github.com/microsoft/onnxruntime && \
cd onnxruntime && \
git submodule update --init --recursive && \
./build.sh --config Release \
  --build_shared_lib \
  --parallel 2 \
  --skip_tests \
  --skip_submodule_sync \
  --allow_running_as_root \
  --cmake_extra_defines CMAKE_CXX_FLAGS="-w" && \
cp build/Linux/Release/libonnxruntime.so* /output/ && \
echo "Build complete! Files in ./onnx-output/"
'
```

This creates two files: `libonnxruntime.so.X.X.X` (the actual library) and `libonnxruntime.so` (a symlink to the versioned file).

### 2. Install on Pi

Copy both library files to the Pi and set up the linker:

```bash
# Copy both library files to Pi
scp onnx-output/libonnxruntime.so* observer@ROOT-Observer.local:/tmp/

# Install and configure on Pi
ssh observer@ROOT-Observer.local 'sudo mv /tmp/libonnxruntime.so* /usr/local/lib/ && \
  sudo ln -sf /usr/local/lib/libonnxruntime.so /usr/local/lib/onnxruntime.so && \
  echo "/usr/local/lib" | sudo tee /etc/ld.so.conf.d/local.conf && \
  sudo ldconfig'
```

This installs both files to `/usr/local/lib`, creates the required `onnxruntime.so` symlink (needed by the Go bindings), and updates the dynamic linker cache.

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

Uses ONNX for basic event detection. Inspired by [Secluso's](https://github.com/secluso/secluso) implementation. 

### Pairing

The firmware uses Bluetooth Low Energy for providing endpoints needed during the pairing process.

### Record

Handles recording video and audio via the camera and microphone components. Camera and microphone (if enabled) input is constantly being read and fanned out to multiple consumers (e.g. stream and recording).

### Relaycomm

Communication with the device the firmware runs on happens via a relay. In the `relaycomm` package, the WebSocket connection is being handled.

### SFX

Play sound effects.

### Storage

Save recordings and update the event log.

### Updater

Check for and download firmware updates.

### WiFi

Scan for WiFi networks and establish a wifi connection.