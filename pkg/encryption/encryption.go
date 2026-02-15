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

// SessionFromKey creates session from a key (e.g. shared secret)
func SessionFromKey(key []byte) (*Session, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("shared secret must be 32 bytes")
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	return &Session{gcm: gcm}, nil
}

// Encrypt encrypts plaintext using AES-256-GCM with optional AAD.
// Format: [nonce][ciphertext] (nonce prepended)
func (s *Session) Encrypt(plaintext, aad []byte) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	nonce := make([]byte, s.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}

	return s.gcm.Seal(nonce, nonce, plaintext, aad), nil
}

// Decrypt decrypts raw ciphertext bytes with optional AAD.
func (s *Session) Decrypt(ciphertext, aad []byte) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	nonceSize := s.gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}

	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	return s.gcm.Open(nil, nonce, ciphertext, aad)
}

// ComputeAAD computes AAD from message type, origin ID, and target ID.
func ComputeAAD(msgType, originId, targetId string) []byte {
	h := sha256.Sum256([]byte(msgType + "|" + originId + "|" + targetId))
	return h[:]
}

// EncodeKey converts public key to base64
func EncodeKey(publicKey []byte) string {
	return base64.StdEncoding.EncodeToString(publicKey)
}

// DecodeKey converts base64 key to bytes
func DecodeKey(encoded string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(encoded)
}
