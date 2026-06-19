package config

import (
	"crypto/rand"
	"fmt"
	"io"
	"log"
	"os"
	"sync"

	"github.com/fxamacker/cbor/v2"
	"github.com/gofrs/uuid"

	rootproto "github.com/therealPaulPlay/root-e2ee-protocol/go-server"

	"root-firmware/pkg/fsutil"
	"root-firmware/pkg/globals"
)

type Config struct {
	mu   sync.RWMutex
	data map[string]any
}

var instance *Config
var once sync.Once

// Init initializes the config system and creates the config file if it doesn't exist
func Init() error {
	var err error
	once.Do(func() {
		instance = &Config{
			data: make(map[string]any),
		}
		err = instance.load()
	})
	return err
}

// ResetForTesting resets the config singleton for test isolation
// Should only be used in unit tests
func ResetForTesting() {
	instance = nil
	once = sync.Once{}
}

// Get returns the singleton config instance
func Get() *Config {
	if instance == nil {
		panic("config not initialized - call Init() first")
	}
	return instance
}

func (c *Config) load() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := os.MkdirAll(globals.FirmwareDataDir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	// Read the existing config if present, starting empty on first boot or corruption
	if data, err := os.ReadFile(globals.ConfigPath); err == nil {
		if err := cbor.Unmarshal(data, &c.data); err != nil {
			log.Printf("Config: Corrupted config detected, recreating")
			if err := os.Rename(globals.ConfigPath, globals.ConfigPath+".corrupted"); err != nil {
				log.Printf("Config: Failed to backup corrupted config: %v", err)
			}
			c.data = make(map[string]any) // Reset data to clean state (redundant if nothing changed it in between, but a safety measure for future changes)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("failed to read config: %w", err)
	}

	// Generate any required keys that are missing and write the file (this makes it easy to add new default/initial keys later on)
	return c.ensureInitConfigKeys()
}

func (c *Config) ensureInitConfigKeys() error {
	changed := false

	// Product ID
	if _, ok := c.data["id"]; !ok {
		id, err := uuid.NewV4()
		if err != nil {
			return fmt.Errorf("failed to generate product ID: %w", err)
		}
		c.data["id"] = id.String()
		changed = true
	}

	if _, ok := c.data["bluetoothName"]; !ok {
		c.data["bluetoothName"] = "ROOT-Observer-" + generateRandomSuffix(4)
		changed = true
	}

	// Product P-256 keypair for communication
	_, hasPriv := c.data["productPrivateKeyP256"]
	_, hasPub := c.data["productPublicKeyP256"]
	if !hasPriv || !hasPub {
		keypair, err := rootproto.GenerateKeypairP256()
		if err != nil {
			return fmt.Errorf("failed to generate product keypair: %w", err)
		}
		c.data["productPrivateKeyP256"] = keypair.PrivateKey
		c.data["productPublicKeyP256"] = keypair.PublicKey
		changed = true
	}

	// Key for encrypting sensitive on-disk data (e.g. recordings)
	if _, ok := c.data["fileEncryptionKeyAES256"]; !ok {
		fileEncryptionKey := make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, fileEncryptionKey); err != nil {
			return fmt.Errorf("failed to generate file encryption key: %w", err)
		}
		c.data["fileEncryptionKeyAES256"] = fileEncryptionKey
		changed = true
	}

	if changed {
		return c.save()
	}
	return nil
}

func (c *Config) save() error {
	data, err := cbor.Marshal(c.data)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	return fsutil.AtomicWrite(globals.ConfigPath, data, 0644)
}

// SetKey sets a config value and persists to disk
// Pass nil to delete the key
// Values are CBOR-normalized so that GetKey always returns the same types
// as cbor.Unmarshal into map[string]any (e.g. []any instead of []string)
func (c *Config) SetKey(key string, value any) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if value == nil {
		delete(c.data, key)
	} else {
		normalized, err := cborNormalize(value)
		if err != nil {
			return fmt.Errorf("failed to normalize config value: %w", err)
		}
		c.data[key] = normalized
	}

	return c.save()
}

// cborNormalize round-trips a value through CBOR so the in-memory type
// matches what cbor.Unmarshal would produce
func cborNormalize(value any) (any, error) {
	b, err := cbor.Marshal(value)
	if err != nil {
		return nil, err
	}
	var normalized any
	if err := cbor.Unmarshal(b, &normalized); err != nil {
		return nil, err
	}
	return normalized, nil
}

// GetKey retrieves a config value
// Returns the value and a boolean indicating if the key exists
func (c *Config) GetKey(key string) (any, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	value, exists := c.data[key]
	return value, exists
}

// GetProductPrivateKeyP256 retrieves the product's P-256 private key
func (c *Config) GetProductPrivateKeyP256() ([]byte, error) {
	return c.getBytesKey("productPrivateKeyP256")
}

// GetProductPublicKeyP256 retrieves the product's P-256 public key
func (c *Config) GetProductPublicKeyP256() ([]byte, error) {
	return c.getBytesKey("productPublicKeyP256")
}

// GetFileEncryptionKeyAES256 retrieves the key used to encrypt sensitive on-disk data
func (c *Config) GetFileEncryptionKeyAES256() ([]byte, error) {
	return c.getBytesKey("fileEncryptionKeyAES256")
}

// getBytesKey retrieves a config value that is expected to be raw bytes
func (c *Config) getBytesKey(configKey string) ([]byte, error) {
	val, ok := c.GetKey(configKey)
	if !ok {
		return nil, fmt.Errorf("%s not set", configKey)
	}
	b, ok := val.([]byte)
	if !ok {
		return nil, fmt.Errorf("%s has invalid type", configKey)
	}
	return b, nil
}
