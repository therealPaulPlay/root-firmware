package encryption

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"sync"

	"golang.org/x/crypto/curve25519"
)

// EncryptionSession caches cipher objects for performance
type EncryptionSession struct {
	key []byte
	gcm cipher.AEAD
	mu  sync.Mutex
}

type KeyPair struct {
	PublicKey  []byte
	PrivateKey []byte
}

// GenerateKeyPair creates a new Curve25519 key pair
func GenerateKeyPair() (*KeyPair, error) {
	privateKey := make([]byte, 32)
	if _, err := rand.Read(privateKey); err != nil {
		return nil, err
	}

	publicKey, err := curve25519.X25519(privateKey, curve25519.Basepoint)
	if err != nil {
		return nil, err
	}

	return &KeyPair{PublicKey: publicKey, PrivateKey: privateKey}, nil
}

// DeriveSharedSecret computes shared secret from your private key and their public key
func DeriveSharedSecret(privateKey, theirPublicKey []byte) ([]byte, error) {
	sharedSecret, err := curve25519.X25519(privateKey, theirPublicKey)
	if err != nil {
		return nil, err
	}

	hash := sha256.Sum256(sharedSecret)
	return hash[:], nil
}

// New creates an encryption session with cached cipher objects
func New(key []byte) (*EncryptionSession, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("key must be 32 bytes")
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	return &EncryptionSession{key: key, gcm: gcm}, nil
}

// Encrypt encrypts plaintext using AES-256-GCM
// Format: [nonce][ciphertext] (nonce is prepended for decryption)
// Returns base64-encoded result
func (s *EncryptionSession) Encrypt(plaintext []byte) (string, error) {
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
// Extracts prepended nonce and decrypts
func (s *EncryptionSession) Decrypt(ciphertextB64 string) ([]byte, error) {
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
	if len(key) != 32 {
		return nil, fmt.Errorf("invalid key length")
	}
	return key, nil
}
