# ROOT (Firmware)

Firmware for ROOT camera devices. Written in Go with a focus on privacy, security, and observability.

## Features and benefits

This firmware turns a Raspberry Pi into a private home security camera. It utilizes end-2-end encryption with forward secrecy and connects to a [relay server](https://github.com/therealPaulPlay/root-relay) to communicate with the [web and mobile app](https://github.com/therealPaulPlay/root-site).

The relay server solely relays the data that's sent across and has no ability to decrypt it. During setup, the relay server URL can be determined, making it easy to choose a self-hosted instance. 

In the ROOT Connect web or mobile app, you can connect to paired cameras, view recorded events, logs, and health metrics. Adjust settings like microphone, event recording preferences, and stream video and audio with low latency.

## Contributing

Contributions are welcome. For inquiries, please reach out via [email](mailto:paulplaystudio@gmail.com) or Bluesky (@paulplay.bsky.social).

## Tested Raspberry Pi models

- Pi Zero 2w

## Building

To build the firmware with a specific version:

```bash
go build -ldflags="-X 'root-firmware/pkg/globals.FirmwareVersion=1.0.0'" -o root-firmware cmd/main.go
```

The version is injected at build time via the `-ldflags` flag. If you build without specifying a version, it defaults to `dev`. If you want to cross-compile (e.g. build on a Mac or Windows computer for the Pi), you need to prepend `GOOS=linux GOARCH=arm GOARM=7` to the commmand. However, building on the Pi itself via the deploy script is fast and recommended.

## Deploying to the Pi

Prerequisites: Create a user `observer`, set the hostname to `ROOT-Observer.local`, install the Go language and the ONNX Runtime on the Pi. Raspian OS Bookworm or higher is required.

```bash
./deploy.sh
```

This syncs the source to `/home/observer/firmware-repository`, builds on the Pi, copies the binary to `/home/observer/root-firmware`, and auto-starts the firmware service via systemd.

Check if running:

```bash
ssh observer@ROOT-Observer.local 'pgrep -f root-firmware'
```

### Setting up an SSH key

Using an SSH key for development instead of an SSH password is significantly more convenient. Create one using `ssh-keygen -t ed25519 -C "your_email@example.com"` and copy it onto the Pi using `ssh-copy-id observer@ROOT-Observer.local`. 

## Installing the ONNX runtime on the Pi

Since the camera hardware is slow compared to modern computers, compiling inside a docker container on a fast machine is recommended over compiling on the single-board computer itself.

### 1. Compile the ONNX runtime

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

### 2. Install the runtime on the Pi

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

Check for and download firmware updates via RAUC.

### WiFi

Scan for WiFi networks and establish a wifi connection.

## CI/CD

Publishing a GitHub Release triggers a workflow that builds the firmware, creates a full firmware image and a RAUC update bundle, and uploads both to S3.

**Required secrets:**

| Secret | Description |
|--------|-------------|
| `RAUC_CA_CERT` | CA certificate for bundle verification (`ca.cert.pem`) |
| `RAUC_SIGNING_CERT` | Certificate for signing bundles |
| `RAUC_SIGNING_KEY` | Private key for signing bundles |
| `S3_ACCESS_KEY_ID` | Access key |
| `S3_SECRET_ACCESS_KEY` | Secret |
| `S3_BUCKET_NAME` | S3 bucket name |
| `S3_REGION` | S3 region (e.g., `fra1`) |
| `S3_DOMAIN` | S3 domain (e.g., `digitaloceanspaces.com`) |

Generate signing certificates with `./build/scripts/generate-certs.sh`.