# BLE GATT API Documentation

## Service UUID
`a07498ca-ad5b-474e-940d-16f1fbe7e8cd`

## Device Discovery

All ROOT cameras advertise with BLE names starting with `ROOT-` (e.g., `ROOT-Observer`).

## Response Format

**All successful responses include `"success": true`** automatically:

```json
{
  "success": true,
  "field": "value"
}
```

Error responses use `"success": false`:

```json
{
  "success": false,
  "error": "Error message"
}
```

## Message Size Limit

Maximum BLE message size: **512 bytes**. Messages exceeding this limit will fail with `"message too large"` error.

## Pairing Flow

The pairing process uses QR code verification to prove physical presence:

1. **Phone reads pairing code**: Read from Get Pairing Code characteristic to get UUID
2. **Phone displays QR code**: Generate and display QR code containing the UUID
3. **User shows QR to camera**: Physically point camera at phone screen
4. **Phone triggers scan**: Write to Scan QR Code characteristic to trigger camera scan
5. **Camera verifies internally**: Camera scans QR, verifies match, sets internal flag
6. **Phone sends pairing request**: Write to Pair Device characteristic (no code needed)
7. **Camera checks verification**: Camera verifies QR was scanned before completing pairing

This ensures a compromised IoT device cannot pair remotely - physical access is required.

## Characteristics

### 1. Get Pairing Code
**UUID**: `51ff12bb-3ed8-46e5-b4f9-d64e2fec021b`
**Properties**: Read
**Description**: Returns a UUID-based pairing code. Code expires after 5 minutes.

**Response**:
```json
{
  "success": true,
  "code": "550e8400-e29b-41d4-a716-446655440000"
}
```

---

### 2. Scan QR Code
**UUID**: `2c8b0a8e-5f3d-4a9b-8e7c-1d4f6a8b9c2e`
**Properties**: Write
**Description**: Triggers camera to capture a frame and scan for QR code. Verifies the scanned code matches the expected pairing code and marks it as verified.

**Request**: Empty

**Response**:
```json
{
  "success": true
}
```

**Error Response**:
```json
{
  "success": false,
  "error": "rate limited: wait 1s between scans"
}
```

---

### 3. Pair Device
**UUID**: `4fafc201-1fb5-459e-8fcc-c5c9c331914b`
**Properties**: Write, Read
**Description**: Pairs a device after QR code verification. Write to initiate pairing, then read to get result. Requires prior successful QR scan.

**Request**:
```json
{
  "deviceId": "unique-device-id",
  "deviceName": "My Phone",
  "devicePublicKey": "base64-encoded-public-key"
}
```

**Response**:
```json
{
  "success": true,
  "data": {
    "productId": "camera-product-id",
    "cameraPublicKey": "base64-encoded-public-key",
    "wifiConnected": true,
    "currentWifiSSID": "MyNetwork",
    "relayDomain": "relay.example.com"
  }
}
```

---

### 4. Get WiFi Networks (Paginated)
**UUID**: `c2be2bc9-cee3-40ae-af50-f9959f25ee5b`
**Properties**: Read
**Description**: Returns WiFi networks one at a time. First read scans, subsequent reads return next network. Read after `hasMore: false` triggers fresh scan.

**Response**:
```json
{
  "success": true,
  "network": {
    "ssid": "Network1",
    "signal": 85,
    "secured": true,
    "unsupported": false
  },
  "hasMore": true
}
```

**Usage**: Keep reading until `hasMore: false`. To refresh, read again.

---

### 5. Set WiFi
**UUID**: `beb5483e-36e1-4688-b7f5-ea07361b26a8`
**Properties**: Write
**Description**: Configures WiFi credentials. Requires encrypted payload from paired device.

**Request**:
```json
{
  "deviceId": "unique-device-id",
  "encryptedPayload": "base64-encrypted-json"
}
```

**Encrypted Payload**:
```json
{
  "ssid": "MyWiFi",
  "password": "wifi-password",
  "countryCode": "US"
}
```

**Note**: `countryCode` is optional but recommended for regulatory compliance. It should be an ISO 3166-1 alpha-2 country code (e.g., "US", "GB", "DE"). The client can obtain this from the browser's locale or geolocation.

**Response**:
```json
{
  "success": true
}
```

---

### 6. Set Relay
**UUID**: `cba1d466-344c-4be3-ab3f-189f80dd7518`
**Properties**: Write
**Description**: Configures relay server domain. Requires encrypted payload from paired device.

**Request**:
```json
{
  "deviceId": "unique-device-id",
  "encryptedPayload": "base64-encrypted-json"
}
```

**Encrypted Payload**:
```json
{
  "relayDomain": "relay.example.com"
}
```

**Response**:
```json
{
  "success": true
}
```

---

### 7. Get Status
**UUID**: `8d8218b6-97bc-4527-a8db-13094ac06b1d`
**Properties**: Read
**Description**: Returns current device status including firmware version and connectivity.

**Response**:
```json
{
  "success": true,
  "version": "1.0.0",
  "wifiConnected": true,
  "relayDomain": "relay.example.com"
}
```
