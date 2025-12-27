# BLE GATT API Documentation

## Service UUID
`a07498ca-ad5b-474e-940d-16f1fbe7e8cd`

## Characteristics

### 1. Get Pairing Code
**UUID**: `51ff12bb-3ed8-46e5-b4f9-d64e2fec021b`
**Properties**: Read
**Description**: Triggers the camera to speak the pairing code and returns it as a string.

**Response**: `"1234"` (4-digit code)

---

### 2. Pair Device
**UUID**: `4fafc201-1fb5-459e-8fcc-c5c9c331914b`
**Properties**: Write, Read
**Description**: Write to pair a device, then read to get pairing result.

**Request**:
```json
{
  "deviceId": "unique-device-id",
  "deviceName": "My Phone",
  "code": "1234",
  "devicePublicKey": "base64-encoded-public-key"
}
```

**Response**:
```json
{
  "success": true,
  "data": {
    "cameraPublicKey": "base64-encoded-public-key"
  }
}
```

---

### 3. Set WiFi
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
  "deviceId": "unique-device-id",
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

### 4. Set Relay
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
  "deviceId": "unique-device-id",
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

### 5. Get Status
**UUID**: `8d8218b6-97bc-4527-a8db-13094ac06b1d`
**Properties**: Read
**Description**: Returns current WiFi connection status.

**Response**:
```json
{
  "connected": true,
  "ssid": "MyWiFi"
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

// Get pairing code
const codeChar = await service.getCharacteristic('51ff12bb-3ed8-46e5-b4f9-d64e2fec021b');
const codeValue = await codeChar.readValue();
const code = new TextDecoder().decode(codeValue);
console.log('Pairing code:', code);

// Pair device
const pairChar = await service.getCharacteristic('4fafc201-1fb5-459e-8fcc-c5c9c331914b');
const pairRequest = {
  deviceId: 'my-device-id',
  deviceName: 'My Phone',
  code: code,
  devicePublicKey: 'base64-public-key'
};
await pairChar.writeValue(new TextEncoder().encode(JSON.stringify(pairRequest)));

// Read pairing result
const resultValue = await pairChar.readValue();
const result = JSON.parse(new TextDecoder().decode(resultValue));
console.log('Pairing result:', result.data);

// Set WiFi (with encryption)
const wifiChar = await service.getCharacteristic('beb5483e-36e1-4688-b7f5-ea07361b26a8');
const wifiRequest = {
  deviceId: 'my-device-id',
  encryptedPayload: encryptedBase64String
};
await wifiChar.writeValue(new TextEncoder().encode(JSON.stringify(wifiRequest)));
```
