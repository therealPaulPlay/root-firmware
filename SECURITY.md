## Encryption

**Key exchange**: ECDH using the P-256 curve. Shared secrets are derived via HKDF-SHA256.

**Symmetric encryption**: AES-256-GCM with random 12-byte nonces prepended to ciphertext.

**Forward secrecy**: Clients can initiate key renewal at any time. When renewed, fresh keypairs are generated and old keys are discarded after a 30-second grace period for in-flight messages.

## Pairing

Pairing is performed over Bluetooth Low Energy (BLE) and requires physical proximity plus human verification.

1. The app connects to the camera's BLE service
2. Camera generates a unique pairing code (UUID) valid for 15 minutes
3. The app displays this code as a QR code
4. The user holds their phone in front of the camera
5. The camera scans and verifies the QR code using its own lens (rate-limited to prevent brute force)
6. Upon verification, the app sends its public key; the camera responds with its public key
7. Both derive a shared secret for encrypted communication

The QR verification step ensures a human is physically present and in control of the pairing process. This prevents automated pairing attempts from nearby IoT devices or other Bluetooth-enabled systems.

Multiple devices can pair with a single camera. Each paired device has its own public key stored on the camera.

### Post-pairing BLE communication

Sensitive BLE operations (WiFi credentials, relay server configuration) are encrypted using the established shared secret. The paired device sends its device ID and an encrypted payload; the camera decrypts using the corresponding shared secret.

## Network architecture

**Standalone operation**: Each camera operates independently without requiring a hub or gateway.

**Relay communication**: All remote communication flows through a configurable relay server via secure WebSocket (WSS). The camera initiates all outbound connections.

**Zero-knowledge relay**: The relay server only routes encrypted messages between cameras and paired devices. It has no access to decryption keys and cannot read the content.

**No inbound connections**: The camera does not expose any listening ports. All connections are outbound to the relay server over HTTPS/WSS.

## Reporting vulnerabilities

Report security issues via [email](mailto:paulplaystudio@gmail.com).