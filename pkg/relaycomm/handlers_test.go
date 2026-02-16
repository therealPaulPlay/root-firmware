package relaycomm

import (
	"sync"
	"testing"

	"root-firmware/pkg/config"
	"root-firmware/pkg/devices"
	"root-firmware/pkg/encryption"
	"root-firmware/pkg/testutil"

	"github.com/fxamacker/cbor/v2"
)

func resetAllSingletons() {
	if instance != nil {
		instance.Stop()
	}
	instance = nil
	once = sync.Once{}
	devices.ResetForTesting()
	config.ResetForTesting()
}

func setupHandlerTest(t *testing.T) func() {
	t.Helper()
	resetAllSingletons()

	cleanupGlobals := testutil.SetupTempGlobals(t)

	if err := config.Init(); err != nil {
		t.Fatalf("config.Init() error = %v", err)
	}
	devices.Init()

	return func() {
		cleanupGlobals()
		resetAllSingletons()
	}
}

func TestErrorPayload(t *testing.T) {
	payload := errorPayload("ERR_CODE", "Something went wrong")

	if payload["success"] != false {
		t.Errorf("success = %v, want false", payload["success"])
	}
	if payload["errorCode"] != "ERR_CODE" {
		t.Errorf("errorCode = %v, want ERR_CODE", payload["errorCode"])
	}
	if payload["error"] != "Something went wrong" {
		t.Errorf("error = %v, want 'Something went wrong'", payload["error"])
	}
}

func TestUseEncryption_DeviceNotPaired(t *testing.T) {
	cleanup := setupHandlerTest(t)
	defer cleanup()

	Init() // Initialize relaycomm to enable Send

	handlerCalled := false
	handler := useEncryption("test", func(ctx *HandlerContext, payload []byte) {
		handlerCalled = true
	})

	// Call with non-existent device
	handler(Message{
		Type:      "test",
		OriginID:  "unknown-device",
		TargetID:  "product",
		RequestID: "req-1",
	})

	if handlerCalled {
		t.Error("handler should not be called for unpaired device")
	}
}

func TestUseEncryption_Success(t *testing.T) {
	cleanup := setupHandlerTest(t)
	defer cleanup()

	Init()

	// Generate device keypair
	deviceKeypair, _ := encryption.GenerateKeypair()

	// Get product's public key to derive shared secret
	productPubKeyEncoded, _ := config.Get().GetKey("productPublicKey")
	productPubKey, _ := encryption.DecodeKey(productPubKeyEncoded.(string))

	// Add device with its public key
	devices.Get().Add("test-device", "Test Device", deviceKeypair.PublicKey)

	// Derive shared secret from device's perspective
	sharedSecret, _ := encryption.DeriveSharedSecret(deviceKeypair.PrivateKey, productPubKey)
	deviceSession, _ := encryption.SessionFromKey(sharedSecret)

	// Create encrypted payload
	payloadData := map[string]string{"message": "hello"}
	payloadCBOR, _ := cbor.Marshal(payloadData)

	productID, _ := config.Get().GetKey("id")
	aad := encryption.ComputeAAD("test", "test-device", productID.(string))
	encryptedPayload, _ := deviceSession.Encrypt(payloadCBOR, aad)

	var receivedPayload []byte
	var receivedCtx *HandlerContext
	handler := useEncryption("test", func(ctx *HandlerContext, payload []byte) {
		receivedCtx = ctx
		receivedPayload = payload
	})

	handler(Message{
		Type:      "test",
		OriginID:  "test-device",
		TargetID:  productID.(string),
		RequestID: "req-123",
		Payload:   encryptedPayload,
	})

	if receivedCtx == nil {
		t.Fatal("handler was not called")
	}
	if receivedCtx.DeviceID != "test-device" {
		t.Errorf("DeviceID = %s, want test-device", receivedCtx.DeviceID)
	}
	if receivedCtx.RequestID != "req-123" {
		t.Errorf("RequestID = %s, want req-123", receivedCtx.RequestID)
	}

	// Verify decrypted payload
	var decrypted map[string]string
	cbor.Unmarshal(receivedPayload, &decrypted)
	if decrypted["message"] != "hello" {
		t.Errorf("decrypted message = %s, want hello", decrypted["message"])
	}
}

func TestUseEncryption_DecryptionFailed(t *testing.T) {
	cleanup := setupHandlerTest(t)
	defer cleanup()

	Init()

	// Add device with a public key
	deviceKeypair, _ := encryption.GenerateKeypair()
	devices.Get().Add("test-device", "Test Device", deviceKeypair.PublicKey)

	handlerCalled := false
	handler := useEncryption("test", func(ctx *HandlerContext, payload []byte) {
		handlerCalled = true
	})

	// Send garbage payload that can't be decrypted
	handler(Message{
		Type:      "test",
		OriginID:  "test-device",
		TargetID:  "product",
		RequestID: "req-1",
		Payload:   []byte("invalid encrypted data"),
	})

	if handlerCalled {
		t.Error("handler should not be called when decryption fails")
	}
}
