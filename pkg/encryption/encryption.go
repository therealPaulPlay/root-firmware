package encryption

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"sync"

	"golang.org/x/crypto/hkdf"
)

// Session handles AES-256-GCM encryption
type Session struct {
	gcm cipher.AEAD
	mu  sync.Mutex
}

// Keypair holds public and private keys for key exchange
type Keypair struct {
	PublicKey  []byte
	PrivateKey []byte
}

// GenerateKeypair creates new P-256 keypair for key exchange
func GenerateKeypair() (*Keypair, error) {
	curve := ecdh.P256()
	privateKey, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}

	return &Keypair{
		PublicKey:  privateKey.PublicKey().Bytes(),
		PrivateKey: privateKey.Bytes(),
	}, nil
}

// DeriveSharedSecret computes shared secret from P-256 ECDH using HKDF
func DeriveSharedSecret(yourPrivateKey, theirPublicKey []byte) ([]byte, error) {
	curve := ecdh.P256()

	// Parse private key
	privKey, err := curve.NewPrivateKey(yourPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("invalid private key: %w", err)
	}

	// Parse public key
	pubKey, err := curve.NewPublicKey(theirPublicKey)
	if err != nil {
		return nil, fmt.Errorf("invalid public key: %w", err)
	}

	// Perform ECDH
	secret, err := privKey.ECDH(pubKey)
	if err != nil {
		return nil, fmt.Errorf("ECDH failed: %w", err)
	}

	// Use HKDF to derive key material
	hkdfReader := hkdf.New(sha256.New, secret, nil, []byte("root-camera-encryption"))
	key := make([]byte, 32)
	if _, err := io.ReadFull(hkdfReader, key); err != nil {
		return nil, err
	}

	return key, nil
}

// FromSharedSecret creates session from shared secret
func FromSharedSecret(sharedSecret []byte) (*Session, error) {
	if len(sharedSecret) != 32 {
		return nil, fmt.Errorf("shared secret must be 32 bytes")
	}

	block, err := aes.NewCipher(sharedSecret)
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	return &Session{gcm: gcm}, nil
}

// Encrypt encrypts plaintext using AES-256-GCM
// Format: [nonce][ciphertext] (nonce prepended)
// Returns base64-encoded result
func (s *Session) Encrypt(plaintext []byte) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	nonce := make([]byte, s.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}

	ciphertext := s.gcm.Seal(nonce, nonce, plaintext, nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Decrypt decrypts base64-encoded ciphertext
func (s *Session) Decrypt(ciphertextB64 string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ciphertext, err := base64.StdEncoding.DecodeString(ciphertextB64)
	if err != nil {
		return nil, err
	}

	nonceSize := s.gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}

	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	return s.gcm.Open(nil, nonce, ciphertext, nil)
}

// EncodePublicKey converts public key to base64
func EncodePublicKey(publicKey []byte) string {
	return base64.StdEncoding.EncodeToString(publicKey)
}

// DecodePublicKey converts base64 public key to bytes
func DecodePublicKey(encoded string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	// P-256 public keys are 65 bytes (uncompressed) or 33 bytes (compressed)
	if len(key) != 65 && len(key) != 33 {
		return nil, fmt.Errorf("invalid P-256 public key length: got %d bytes, expected 65 (uncompressed) or 33 (compressed)", len(key))
	}
	return key, nil
}
