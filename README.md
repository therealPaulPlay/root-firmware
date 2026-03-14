![ROOT logo](logo.svg)

# ROOT (Firmware)

Firmware for ROOT camera products. Written in Go with a focus on privacy, security, and observability.

## Features and benefits

This firmware turns a Raspberry Pi into a private home security camera. It utilizes end-2-end encryption with forward secrecy and connects to a [relay server](https://github.com/therealPaulPlay/root-relay) to communicate with the [web and mobile app](https://github.com/therealPaulPlay/root-site).

The relay server solely relays the data that's sent across and has no ability to decrypt it. During setup, the relay server URL can be determined, making it easy to choose a self-hosted instance. This allows for remotely accessing cameras in a secure and convenient way.

The ROOT Connect web and mobile app lets you connect to paired cameras, stream video and audio with low latency, view recorded events, logs, and health metrics, and adjust settings like microphone, event recording preferences, and push notifications.

## Install on your own Pi

Please refer to [this installation guide](https://rootprivacy.com/blog/building-your-own-security-camera), where you can find download links for the latest prebuilt images.

### Supported Raspberry Pi models

- Pi Zero 2w

## Contributing

Contributions are welcome. For inquiries, please reach out via [email](mailto:paulplaystudio@gmail.com).

## Building

**Build dependency:** [Go](https://go.dev/doc/install)

**Runtime dependencies:**
- ONNX Runtime (see [Installing ONNX runtime](#onnx-runtime))
- OpenH264 (see [Installing OpenH264](#openh264))
- FFmpeg
- rpicam-apps
- BlueZ
- RAUC (only needed for OTA updates)
- alsa-utils
- i2c-tools (and ensure i2c is enabled)

Build the firmware:

```bash
go build -ldflags="-X 'root-firmware/pkg/globals.FirmwareVersion=1.0.0'" -o root-firmware cmd/main.go
```

The version is injected at build time via `-ldflags`. Without it, the version defaults to `dev`. To cross-compile for the Pi (64-bit), prepend `GOOS=linux GOARCH=arm64`.

## Deploying to a Pi for development

Prerequisites: Create a user `observer`, set the hostname to `ROOT-Observer`, and ensure SSH is enabled.

> [!TIP]
> Using an SSH key for development instead of a password is significantly more convenient.
> Create one using `ssh-keygen -t ed25519 -C "your_email@example.com"` and copy it over using `ssh-copy-id observer@ROOT-Observer.local`.

Most dependencies listed in [Building](#building) can be installed via `apt`. The ones that require manual setup are explained below.

### ONNX runtime

Compiling inside a docker container on a fast machine is recommended over compiling on the SBC itself. A precompiled version can be found under `build/libs-precompiled/`; if you prefer using that you can skip step 1 and adjust the folder paths accordingly for step 2. 

**1. Compile the ONNX runtime for the Pi**

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

**2. Install the runtime on the Pi**

```bash
# Copy both library files to Pi
scp onnx-output/libonnxruntime.so* observer@ROOT-Observer.local:/tmp/

# Install and configure on Pi
ssh observer@ROOT-Observer.local 'sudo mv /tmp/libonnxruntime.so* /usr/local/lib/ && \
  sudo ln -sf /usr/local/lib/libonnxruntime.so /usr/local/lib/onnxruntime.so && \
  echo "/usr/local/lib" | sudo tee /etc/ld.so.conf.d/local.conf && \
  sudo ldconfig'
```

This script copies all output files over to the Pi, sets up the symlink for `onnxruntime.so`, and updates the dynamic linker cache.

### OpenH264

A precompiled binary from [OpenH264 GitHub](https://github.com/cisco/openh264/releases) is included in `build/libs-precompiled/`. Use the script below to set it up.

```bash
scp build/libs-precompiled/libopenh264-2.5.1-linux-arm64.7.so observer@ROOT-Observer.local:/tmp/

ssh observer@ROOT-Observer.local 'sudo mv /tmp/libopenh264-2.5.1-linux-arm64.7.so /usr/local/lib/ && \
  sudo ln -sf /usr/local/lib/libopenh264-2.5.1-linux-arm64.7.so /usr/local/lib/libopenh264.so && \
  sudo ldconfig'
```

### Deploy script

```bash
./deploy.sh
```

This syncs the source to `~/firmware-repository`, builds on the Pi, copies the binary to `~/root-firmware`, and auto-starts the firmware service via systemd.

Check if running:

```bash
ssh observer@ROOT-Observer.local 'pgrep -f root-firmware'
```

## Package overview

| Package | Description |
|---------|-------------|
| Config | Store and persist configuration values at runtime. |
| Devices | Manage paired devices. |
| Encryption | Create encryption keys, encrypt and decrypt values, Diffie-Hellman key exchange helpers. |
| Globals | Constants that are reused across packages. |
| Logger | Collect logs and store them in a JSON for easy access. |
| ML | ONNX-based event detection. Inspired by [Secluso's](https://github.com/secluso/secluso) implementation. |
| Notifications | Send notifications to the relay server's notification router. |
| Pairing | Bluetooth Low Energy endpoints for the pairing process. |
| Record | Record video and audio. Input from camera and mic is constantly read and fanned out to multiple consumers (e.g. stream and/or recording). |
| Relaycomm | Communicate with the relay server via WebSockets. |
| SFX | Play sound effects. |
| Storage | Save recordings and update the event log. |
| Testutil | Utilities used for unit testing. |
| Updater | Check for, download and install firmware updates utilizing RAUC. |
| WiFi | Scan for WiFi networks and establish a connection. |
| Metrics | Performance, memory utilization, and disk usage statistics. |

## CI/CD

Publishing a GitHub Release triggers a workflow that builds the firmware, creates a full firmware image and a RAUC update bundle, and uploads both to S3 (the workflow is built with Digital Ocean Spaces in mind).

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