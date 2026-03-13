# BLE GATT API

Service UUID: `a07498ca-ad5b-474e-940d-16f1fbe7e8cd`

## Overview

- All messages are CBOR-encoded
- MTU limit: ~128 bytes
- Device discovery: BLE names start with `ROOT-`

## Pairing flow

1. Read **Pairing code** → receive UUID
2. Display UUID as QR code on phone
3. Point camera at phone screen
4. Read **Scan QR code** → camera verifies
5. Write to **Pair device** with device info
6. Read **Pair device** → confirm success
7. Read **Product public key** → get camera's public key

## Response format

**Success:**
```cbor
{success: true, ...fields}
```

**Error:**
```cbor
{success: false, error: "message"}
```

## Characteristics

### Product model
UUID: `fa3fd066-de2d-4a15-8e0c-4a8d45a847a5`
Read only

| Field | Type | Description |
|-------|------|-------------|
| model | string | Product model name |

### Product ID
UUID: `8f3c4d5e-9a2b-4f1e-8d6c-7e5f4a3b2c1d`
Read only

| Field | Type | Description |
|-------|------|-------------|
| productId | string | Unique product UUID |

### Pairing code
UUID: `51ff12bb-3ed8-46e5-b4f9-d64e2fec021b`
Read only

| Field | Type | Description |
|-------|------|-------------|
| code | string | UUID valid for 15 minutes |

### Scan QR code
UUID: `2c8b0a8e-5f3d-4a9b-8e7c-1d4f6a8b9c2e`
Read only

Triggers camera to scan for QR code. Returns success if code matches.

Rate limited: 1 scan per second.

### Pair device
UUID: `4fafc201-1fb5-459e-8fcc-c5c9c331914b`
Write then Read

**Write:**
| Field | Type | Description |
|-------|------|-------------|
| deviceId | string | Unique device identifier |
| deviceName | string | Human-readable name |
| devicePublicKey | string | Base64-encoded P-256 public key |

**Read:**
Returns success/error status.

### Product public key
UUID: `2d7c0e8f-5a3b-4c1d-8e6a-0f4b9d2c7e1a`
Read only

| Field | Type | Description |
|-------|------|-------------|
| publicKey | string | Base64-encoded P-256 public key |

### Viewfinder
UUID: `3d9e1f7a-4b6c-5e8d-9f0a-1b2c3d4e5f6a`
Read only (chunked)

Returns 3-bit grayscale bitmap (96x54) for QR alignment preview.

| Field | Type | Description |
|-------|------|-------------|
| data | bytes | Chunk of bitmap data |
| index | int | Chunk index |
| hasMore | bool | More chunks available |

### WiFi networks
UUID: `c2be2bc9-cee3-40ae-af50-f9959f25ee5b`
Read only (paginated)

First read triggers scan. Keep reading until `hasMore: false`.

| Field | Type | Description |
|-------|------|-------------|
| network.ssid | string | Network name |
| network.signal | int | Signal strength (0-100) |
| network.secured | bool | Requires password |
| hasMore | bool | More networks available |

### WiFi status
UUID: `d96453d5-1f49-47d6-8cbd-ac5547fc51a9`
Read only

| Field | Type | Description |
|-------|------|-------------|
| connectedSSID | string | Connected network name (empty if not connected) |

### WiFi connect
UUID: `beb5483e-36e1-4688-b7f5-ea07361b26a8`
Write then Read (encrypted)

**Write:**
| Field | Type | Description |
|-------|------|-------------|
| deviceId | string | Paired device ID |
| payload | bytes | AES-GCM encrypted CBOR |

**Decrypted payload:**
| Field | Type | Description |
|-------|------|-------------|
| ssid | string | Network name |
| password | string | Network password |
| countryCode | string | ISO 3166-1 alpha-2 (optional) |

### Relay status
UUID: `a9988b7b-e4ea-49b1-b9d1-548aeb0ec5ab`
Read only

| Field | Type | Description |
|-------|------|-------------|
| relayDomain | string | Configured relay server (null if unset) |

### Relay set
UUID: `cba1d466-344c-4be3-ab3f-189f80dd7518`
Write then Read (encrypted)

**Write:**
| Field | Type | Description |
|-------|------|-------------|
| deviceId | string | Paired device ID |
| payload | bytes | AES-GCM encrypted CBOR |

**Decrypted payload:**
| Field | Type | Description |
|-------|------|-------------|
| relayDomain | string | Relay server domain |