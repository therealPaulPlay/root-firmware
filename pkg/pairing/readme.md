# BLE GATT API Documentation

## Service UUID
`a07498ca-ad5b-474e-940d-16f1fbe7e8cd`

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

**Response**: `"550e8400-e29b-41d4-a716-446655440000"` (UUID string)

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
    "relayDomain": "relay.example.com",
    "availableNetworks": [
      {
        "ssid": "Network1",
        "signal": -45,
        "encrypted": true
      }
    ]
  }
}
```

---

### 4. Set WiFi
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
  "password": "wifi-password"
}
```

**Response**:
```json
{
  "success": true
}
```

---

### 5. Set Relay
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

### 6. Get Status
**UUID**: `8d8218b6-97bc-4527-a8db-13094ac06b1d`
**Properties**: Read
**Description**: Returns current device status including firmware version and connectivity.

**Response**:
```json
{
  "version": "1.0.0",
  "wifiConnected": true,
  "relayDomain": "relay.example.com"
}
```

---

## Web Bluetooth API Example

```javascript
// Connect to device
const device = await navigator.bluetooth.requestDevice({
  filters: [{ namePrefix: 'ROOT-Observer' }],
  optionalServices: ['a07498ca-ad5b-474e-940d-16f1fbe7e8cd']
});

const server = await device.gatt.connect();
const service = await server.getPrimaryService('a07498ca-ad5b-474e-940d-16f1fbe7e8cd');

// Step 1: Get pairing code
const codeChar = await service.getCharacteristic('51ff12bb-3ed8-46e5-b4f9-d64e2fec021b');
const codeValue = await codeChar.readValue();
const code = new TextDecoder().decode(codeValue);
console.log('Pairing code:', code);

// Step 2: Display QR code to user (using a QR code library)
// const qrCode = generateQRCode(code); // Display this to user

// Step 3: Trigger camera to scan the QR code
const scanChar = await service.getCharacteristic('2c8b0a8e-5f3d-4a9b-8e7c-1d4f6a8b9c2e');
await scanChar.writeValue(new Uint8Array([1])); // Trigger scan

// Step 4: Pair device (no code needed - camera already verified QR)
const pairChar = await service.getCharacteristic('4fafc201-1fb5-459e-8fcc-c5c9c331914b');
const pairRequest = {
  deviceId: 'my-device-id',
  deviceName: 'My Phone',
  devicePublicKey: 'base64-public-key'
};
await pairChar.writeValue(new TextEncoder().encode(JSON.stringify(pairRequest)));

// Read pairing result
const resultValue = await pairChar.readValue();
const result = JSON.parse(new TextDecoder().decode(resultValue));
console.log('Pairing result:', result.data);

// Step 5: Set WiFi (with encryption using shared secret)
const wifiChar = await service.getCharacteristic('beb5483e-36e1-4688-b7f5-ea07361b26a8');
const wifiRequest = {
  deviceId: 'my-device-id',
  encryptedPayload: encryptedBase64String // Encrypted with shared secret from ECDH
};
await wifiChar.writeValue(new TextEncoder().encode(JSON.stringify(wifiRequest)));
```
