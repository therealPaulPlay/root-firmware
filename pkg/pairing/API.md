# BLE GATT API documentation

## Service UUID
`a07498ca-ad5b-474e-940d-16f1fbe7e8cd`

## Device discovery

All ROOT cameras advertise with BLE names starting with `ROOT-` (e.g., `ROOT-Observer`).

## Response format

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

## Message size limit

Messages need to stay below ~128 bytes to work across devices. 

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

### 1. Get product ID
**UUID**: `8f3c4d5e-9a2b-4f1e-8d6c-7e5f4a3b2c1d`
**Properties**: Read
**Description**: Returns the unique product ID of the camera. This ID is generated on first boot.

**Response**:
```json
{
  "success": true,
  "productId": "550e8400-e29b-41d4-a716-446655440000"
}
```

---

### 2. Get pairing code
**UUID**: `51ff12bb-3ed8-46e5-b4f9-d64e2fec021b`
**Properties**: Read
**Description**: Generates and returns a new UUID-based pairing code. Code expires after 15 minutes.

**Response**:
```json
{
  "success": true,
  "code": "550e8400-e29b-41d4-a716-446655440000"
}
```

---

### 3. Scan QR code
**UUID**: `2c8b0a8e-5f3d-4a9b-8e7c-1d4f6a8b9c2e`
**Properties**: Read
**Description**: Triggers camera to capture a frame and scan for QR code. Verifies the scanned code matches the expected pairing code and marks it as verified.

**Response**:
```json
{
  "success": true
}
```

**Error response**:
```json
{
  "success": false,
  "error": "rate limited: wait 1s between scans"
}
```

---

### 4. Pair device
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
  "productId": "camera-product-id"
}
```

**Note**: Camera public key must be retrieved from the Get Camera Public Key characteristic. WiFi and relay status can be queried separately using the WiFi Status and Relay Status characteristics.

---

### 5. Get product public key
**UUID**: `2d7c0e8f-5a3b-4c1d-8e6a-0f4b9d2c7e1a`
**Properties**: Read
**Description**: Returns the product's (camera's) public key after successful pairing. Must be read after pairing completes.

**Response**:
```json
{
  "success": true,
  "publicKey": "base64-encoded-public-key"
}
```

---

### 6. Get WiFi networks (Paginated)
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

### 7. Get WiFi status
**UUID**: `d96453d5-1f49-47d6-8cbd-ac5547fc51a9`
**Properties**: Read
**Description**: Returns current WiFi connection status and SSID.

**Response**:
```json
{
  "success": true,
  "connected": true,
  "ssid": "MyNetwork"
}
```

---

### 8. Set WiFi
**UUID**: `beb5483e-36e1-4688-b7f5-ea07361b26a8`
**Properties**: Write, Read
**Description**: Configures WiFi credentials. Write operation blocks until connection completes, then read to verify success. Requires encrypted payload from paired device.

**Write request**:
```json
{
  "deviceId": "unique-device-id",
  "payload": "base64-encrypted-json"
}
```

**Encrypted payload**:
```json
{
  "ssid": "MyWiFi",
  "password": "wifi-password",
  "countryCode": "US"
}
```

**Note**: `countryCode` is optional but recommended for regulatory compliance. It should be an ISO 3166-1 alpha-2 country code (e.g., "US", "GB", "DE"). The client can obtain this from the browser's locale or geolocation.

**Read response (Success)**:
```json
{
  "success": true
}
```

**Read response (Failure)**:
```json
{
  "success": false,
  "error": "WiFi connection failed"
}
```

---

### 9. Get relay status
**UUID**: `a9988b7b-e4ea-49b1-b9d1-548aeb0ec5ab`
**Properties**: Read
**Description**: Returns currently configured relay server domain.

**Response**:
```json
{
  "success": true,
  "relayDomain": "relay.example.com"
}
```

**Note**: `relayDomain` will be `null` if not configured.

---

### 10. Set relay
**UUID**: `cba1d466-344c-4be3-ab3f-189f80dd7518`
**Properties**: Write, Read
**Description**: Configures relay server domain. Write operation blocks until configuration completes, then read to verify success. Requires encrypted payload from paired device.

**Write request**:
```json
{
  "deviceId": "unique-device-id",
  "payload": "base64-encrypted-json"
}
```

**Encrypted payload**:
```json
{
  "relayDomain": "relay.example.com"
}
```

**Read response (Success)**:
```json
{
  "success": true
}
```

**Read response (Failure)**:
```json
{
  "success": false,
  "error": "Relay configuration failed"
}
```

