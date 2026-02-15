# Encoding

CBOR-encoded messages over WebSocket binary frames.

## Message structure

```
Device → Relay → Product:
{
  "type": "getHealth",
  "originId": "device-uuid",
  "targetId": "product-uuid",
  "requestId": "request-uuid",
  "payload": <encrypted CBOR bytes>
}

Product → Relay → Device:
{
  "type": "getHealth",
  "originId": "product-uuid",
  "targetId": "device-uuid",
  "requestId": "request-uuid",
  "payload": <encrypted CBOR bytes>
}
```

The relay routes messages by `targetId` only.

## Key renewal

```
Device                              Product
   │                                   │
   │──renewKey─────────────────────────│  {newPublicKey: base64}
   │  (encrypted with current key)     │
   │←─renewKey response────────────────│  {success: true}
   │                                   │
   │  Device switches to new key       │
   │                                   │
   │──renewKeyAck──────────────────────│  {ack: true}
   │  (encrypted with NEW key)         │
   │←─renewKeyAck response─────────────│  {success: true}
   │                                   │  Previous key valid 30s for in-flight messages
```

## Payload format

```cbor
// Success
{"success": true, ...fields}

// Error
{"success": false, "errorCode": "ERROR_CODE", "error": "message"}
```
