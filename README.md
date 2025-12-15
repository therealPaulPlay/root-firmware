# root-firmware

Firmware for ROOT camera devices. Built in Go with a focus on privacy, security, and observability.

## Package overview

### Config

All configuration values are stored in a config `JSON` file in the `/data`/ partition.

### Devices

The `devices` package is for managing paired devices.

### Encryption

This package exposes functions for creating encryption keys using the Diffie-Hellman key exchange method.

### Globals

Global variables.

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