package encryption

import (
	"bytes"
	"testing"
)

func TestGenerateKeypair(t *testing.T) {
	kp, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() error = %v", err)
	}

	// P-256 public key is 65 bytes (uncompressed point format)
	if len(kp.PublicKey) != 65 {
		t.Errorf("PublicKey length = %d, want 65", len(kp.PublicKey))
	}

	// P-256 private key is 32 bytes
	if len(kp.PrivateKey) != 32 {
		t.Errorf("PrivateKey length = %d, want 32", len(kp.PrivateKey))
	}
}

func TestGenerateKeypair_UniqueKeys(t *testing.T) {
	kp1, _ := GenerateKeypair()
	kp2, _ := GenerateKeypair()

	if bytes.Equal(kp1.PrivateKey, kp2.PrivateKey) {
		t.Error("generated keypairs should have unique private keys")
	}
	if bytes.Equal(kp1.PublicKey, kp2.PublicKey) {
		t.Error("generated keypairs should have unique public keys")
	}
}

func TestDeriveSharedSecret(t *testing.T) {
	// Generate two keypairs (simulating two parties)
	alice, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() error = %v", err)
	}
	bob, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() error = %v", err)
	}

	// Both should derive the same shared secret
	aliceSecret, err := DeriveSharedSecret(alice.PrivateKey, bob.PublicKey)
	if err != nil {
		t.Fatalf("DeriveSharedSecret(alice) error = %v", err)
	}

	bobSecret, err := DeriveSharedSecret(bob.PrivateKey, alice.PublicKey)
	if err != nil {
		t.Fatalf("DeriveSharedSecret(bob) error = %v", err)
	}

	if !bytes.Equal(aliceSecret, bobSecret) {
		t.Error("shared secrets should be equal")
	}

	// Shared secret should be 32 bytes (AES-256 key)
	if len(aliceSecret) != 32 {
		t.Errorf("shared secret length = %d, want 32", len(aliceSecret))
	}
}

func TestDeriveSharedSecret_InvalidPrivateKey(t *testing.T) {
	kp, _ := GenerateKeypair()

	_, err := DeriveSharedSecret([]byte("invalid"), kp.PublicKey)
	if err == nil {
		t.Error("DeriveSharedSecret() should error with invalid private key")
	}
}

func TestDeriveSharedSecret_InvalidPublicKey(t *testing.T) {
	kp, _ := GenerateKeypair()

	_, err := DeriveSharedSecret(kp.PrivateKey, []byte("invalid"))
	if err == nil {
		t.Error("DeriveSharedSecret() should error with invalid public key")
	}
}

func TestSessionFromKey(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}

	session, err := SessionFromKey(key)
	if err != nil {
		t.Fatalf("SessionFromKey() error = %v", err)
	}
	if session == nil {
		t.Error("SessionFromKey() returned nil session")
	}
}

func TestSessionFromKey_InvalidLength(t *testing.T) {
	tests := []struct {
		name    string
		keyLen  int
	}{
		{"too short", 16},
		{"too long", 64},
		{"empty", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := make([]byte, tt.keyLen)
			_, err := SessionFromKey(key)
			if err == nil {
				t.Errorf("SessionFromKey() should error with %d byte key", tt.keyLen)
			}
		})
	}
}

func TestEncryptDecrypt_Roundtrip(t *testing.T) {
	// Create a session with a test key
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	session, err := SessionFromKey(key)
	if err != nil {
		t.Fatal(err)
	}

	plaintext := []byte("Hello, encrypted world!")
	aad := []byte("additional authenticated data")

	ciphertext, err := session.Encrypt(plaintext, aad)
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}

	// Ciphertext should be longer than plaintext (nonce + auth tag)
	if len(ciphertext) <= len(plaintext) {
		t.Error("ciphertext should be longer than plaintext")
	}

	decrypted, err := session.Decrypt(ciphertext, aad)
	if err != nil {
		t.Fatalf("Decrypt() error = %v", err)
	}

	if !bytes.Equal(decrypted, plaintext) {
		t.Errorf("Decrypt() = %q, want %q", decrypted, plaintext)
	}
}

func TestEncryptDecrypt_EmptyPlaintext(t *testing.T) {
	key := make([]byte, 32)
	session, _ := SessionFromKey(key)

	ciphertext, err := session.Encrypt([]byte{}, nil)
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}

	decrypted, err := session.Decrypt(ciphertext, nil)
	if err != nil {
		t.Fatalf("Decrypt() error = %v", err)
	}

	if len(decrypted) != 0 {
		t.Errorf("Decrypt() = %v, want empty", decrypted)
	}
}

func TestEncryptDecrypt_NilAAD(t *testing.T) {
	key := make([]byte, 32)
	session, _ := SessionFromKey(key)

	plaintext := []byte("test data")

	ciphertext, err := session.Encrypt(plaintext, nil)
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}

	decrypted, err := session.Decrypt(ciphertext, nil)
	if err != nil {
		t.Fatalf("Decrypt() error = %v", err)
	}

	if !bytes.Equal(decrypted, plaintext) {
		t.Error("roundtrip with nil AAD failed")
	}
}

func TestDecrypt_WrongAAD(t *testing.T) {
	key := make([]byte, 32)
	session, _ := SessionFromKey(key)

	plaintext := []byte("secret message")
	correctAAD := []byte("correct")
	wrongAAD := []byte("wrong")

	ciphertext, _ := session.Encrypt(plaintext, correctAAD)

	_, err := session.Decrypt(ciphertext, wrongAAD)
	if err == nil {
		t.Error("Decrypt() should fail with wrong AAD")
	}
}

func TestDecrypt_TamperedCiphertext(t *testing.T) {
	key := make([]byte, 32)
	session, _ := SessionFromKey(key)

	plaintext := []byte("secret message")
	ciphertext, _ := session.Encrypt(plaintext, nil)

	// Tamper with ciphertext
	ciphertext[len(ciphertext)-1] ^= 0xFF

	_, err := session.Decrypt(ciphertext, nil)
	if err == nil {
		t.Error("Decrypt() should fail with tampered ciphertext")
	}
}

func TestDecrypt_CiphertextTooShort(t *testing.T) {
	key := make([]byte, 32)
	session, _ := SessionFromKey(key)

	// GCM nonce is 12 bytes, so anything shorter should fail
	shortCiphertext := []byte{0x01, 0x02, 0x03}

	_, err := session.Decrypt(shortCiphertext, nil)
	if err == nil {
		t.Error("Decrypt() should fail with ciphertext shorter than nonce")
	}
}

func TestEncrypt_UniqueNonces(t *testing.T) {
	key := make([]byte, 32)
	session, _ := SessionFromKey(key)

	plaintext := []byte("same plaintext")

	ct1, _ := session.Encrypt(plaintext, nil)
	ct2, _ := session.Encrypt(plaintext, nil)

	// Same plaintext should produce different ciphertext (due to random nonce)
	if bytes.Equal(ct1, ct2) {
		t.Error("Encrypt() should produce different ciphertext for same plaintext (random nonce)")
	}
}

func TestComputeAAD(t *testing.T) {
	aad1 := ComputeAAD("stream", "device1", "device2")
	aad2 := ComputeAAD("stream", "device1", "device2")
	aad3 := ComputeAAD("control", "device1", "device2")
	aad4 := ComputeAAD("stream", "device2", "device1")

	// Same inputs = same output (deterministic)
	if !bytes.Equal(aad1, aad2) {
		t.Error("identical inputs should produce identical AAD")
	}

	// Different message type = different AAD
	if bytes.Equal(aad1, aad3) {
		t.Error("different message types should produce different AAD")
	}

	// Different order of IDs = different AAD
	if bytes.Equal(aad1, aad4) {
		t.Error("swapped IDs should produce different AAD")
	}

	// Should be SHA-256 length (32 bytes)
	if len(aad1) != 32 {
		t.Errorf("AAD length = %d, want 32", len(aad1))
	}
}


func TestFullKeyExchangeAndEncryption(t *testing.T) {
	// Simulate a full key exchange between two parties
	alice, _ := GenerateKeypair()
	bob, _ := GenerateKeypair()

	// Both derive shared secret
	aliceSecret, _ := DeriveSharedSecret(alice.PrivateKey, bob.PublicKey)
	bobSecret, _ := DeriveSharedSecret(bob.PrivateKey, alice.PublicKey)

	// Create sessions
	aliceSession, _ := SessionFromKey(aliceSecret)
	bobSession, _ := SessionFromKey(bobSecret)

	// Alice encrypts a message for Bob
	message := []byte("Hello Bob, this is Alice!")
	aad := ComputeAAD("chat", "alice", "bob")

	ciphertext, err := aliceSession.Encrypt(message, aad)
	if err != nil {
		t.Fatalf("Alice Encrypt() error = %v", err)
	}

	// Bob decrypts the message
	decrypted, err := bobSession.Decrypt(ciphertext, aad)
	if err != nil {
		t.Fatalf("Bob Decrypt() error = %v", err)
	}

	if !bytes.Equal(decrypted, message) {
		t.Errorf("Bob received %q, want %q", decrypted, message)
	}

	// Bob responds to Alice
	response := []byte("Hi Alice, got your message!")
	responseAAD := ComputeAAD("chat", "bob", "alice")

	responseCiphertext, _ := bobSession.Encrypt(response, responseAAD)
	decryptedResponse, err := aliceSession.Decrypt(responseCiphertext, responseAAD)
	if err != nil {
		t.Fatalf("Alice Decrypt() error = %v", err)
	}

	if !bytes.Equal(decryptedResponse, response) {
		t.Errorf("Alice received %q, want %q", decryptedResponse, response)
	}
}