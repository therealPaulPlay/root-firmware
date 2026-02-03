package config

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"

	"root-firmware/pkg/encryption"
	"root-firmware/pkg/fsutil"
	"root-firmware/pkg/globals"

	"github.com/gofrs/uuid"
)

type Config struct {
	mu   sync.RWMutex
	data map[string]any
}

var instance *Config
var once sync.Once

// Init initializes the config system and creates config.json if it doesn't exist
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

	if _, err := os.Stat(globals.ConfigPath); os.IsNotExist(err) {
		return c.createInitialConfig()
	}

	data, err := os.ReadFile(globals.ConfigPath)
	if err != nil {
		return fmt.Errorf("failed to read config: %w", err)
	}

	if err := json.Unmarshal(data, &c.data); err != nil {
		// Corrupted config - backup and recreate
		log.Printf("Config: Corrupted config detected, recreating")
		os.Rename(globals.ConfigPath, globals.ConfigPath+".corrupted")
		c.data = make(map[string]any)
		return c.createInitialConfig()
	}

	return nil
}

func (c *Config) createInitialConfig() error {
	if err := os.MkdirAll(globals.FirmwareDataDir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	id, err := uuid.NewV4()
	if err != nil {
		return fmt.Errorf("failed to generate device ID: %w", err)
	}

	// Generate product keypair for end-to-end encryption with paired devices
	// Store as base64 encoded strings
	keypair, err := encryption.GenerateKeypair()
	if err != nil {
		return fmt.Errorf("failed to generate product keypair: %w", err)
	}

	c.data = map[string]any{
		"id":                id.String(),
		"bluetoothName":     "ROOT-Observer-" + generateRandomSuffix(4),
		"productPrivateKey": encryption.EncodeKey(keypair.PrivateKey),
		"productPublicKey":  encryption.EncodeKey(keypair.PublicKey),
	}

	return c.save()
}

func (c *Config) save() error {
	data, err := json.MarshalIndent(c.data, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	return fsutil.AtomicWrite(globals.ConfigPath, data, 0644)
}

// SetKey sets a config value and persists to disk
// Pass nil to delete the key
// Values are JSON-normalized so that GetKey always returns the same types
// as json.Unmarshal into map[string]any (e.g. []any instead of []string)
func (c *Config) SetKey(key string, value any) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if value == nil {
		delete(c.data, key)
	} else {
		normalized, err := jsonNormalize(value)
		if err != nil {
			return fmt.Errorf("failed to normalize config value: %w", err)
		}
		c.data[key] = normalized
	}

	return c.save()
}

// jsonNormalize round-trips a value through JSON so the in-memory type
// matches what json.Unmarshal would produce
func jsonNormalize(value any) (any, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var normalized any
	if err := json.Unmarshal(b, &normalized); err != nil {
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

// GetProductPrivateKey retrieves the camera's private key as raw bytes
func (c *Config) GetProductPrivateKey() ([]byte, error) {
	keyEncoded, ok := c.GetKey("productPrivateKey")
	if !ok {
		return nil, fmt.Errorf("product private key not set")
	}
	keyStr, ok := keyEncoded.(string)
	if !ok {
		return nil, fmt.Errorf("product private key has invalid type")
	}
	key, err := encryption.DecodeKey(keyStr)
	if err != nil {
		return nil, fmt.Errorf("failed to decode product private key: %w", err)
	}
	return key, nil
}
