package config

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"

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

	c.data = map[string]any{
		"id":            id.String(),
		"bluetoothName": "ROOT-Observer-" + generateRandomSuffix(4),
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
func (c *Config) SetKey(key string, value any) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if value == nil {
		delete(c.data, key)
	} else {
		c.data[key] = value
	}

	return c.save()
}

// GetKey retrieves a config value
// Returns the value and a boolean indicating if the key exists
func (c *Config) GetKey(key string) (any, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	value, exists := c.data[key]
	return value, exists
}
