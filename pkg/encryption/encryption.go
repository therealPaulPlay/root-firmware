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

// GenerateKeypair creates new Curve25519 keypair for key exchange
func GenerateKeypair() (*Keypair, error) {
	privateKey := make([]byte, 32)
	if _, err := rand.Read(privateKey); err != nil {
		return nil, err
	}

	publicKey, err := curve25519.X25519(privateKey, curve25519.Basepoint)
	if err != nil {
		return nil, err
	}

	return &Keypair{PublicKey: publicKey, PrivateKey: privateKey}, nil
}

// DeriveSharedSecret computes shared secret from keypair exchange using HKDF
func DeriveSharedSecret(yourPrivateKey, theirPublicKey []byte) ([]byte, error) {
	if len(yourPrivateKey) != 32 || len(theirPublicKey) != 32 {
		return nil, fmt.Errorf("keys must be 32 bytes")
	}

	secret, err := curve25519.X25519(yourPrivateKey, theirPublicKey)
	if err != nil {
		return nil, err
	}

	// Check for all-zero shared secret (weak key)
	allZero := true
	for _, b := range secret {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		return nil, fmt.Errorf("weak shared secret detected")
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
	if len(key) != 32 {
		return nil, fmt.Errorf("invalid key length")
	}
	return key, nil
}
