package config

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/gofrs/uuid"
)

const configPath = "/data/.firmware-data/config.json"

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
	defer c.mu.Unlock() // Unlock after function has returned

	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return c.createInitialConfig()
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("failed to read config: %w", err)
	}

	if err := json.Unmarshal(data, &c.data); err != nil {
		return fmt.Errorf("failed to parse config: %w", err)
	}

	return nil
}

func (c *Config) createInitialConfig() error {
	if err := os.MkdirAll("/home/pi/.firmware-data", 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	id, err := uuid.NewV4()
	if err != nil {
		return fmt.Errorf("failed to generate device ID: %w", err)
	}

	c.data = map[string]any{
		"id":               id.String(),
		"firmware_version": "1.0.0",
	}

	return c.save()
}

func (c *Config) save() error {
	data, err := json.MarshalIndent(c.data, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(configPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	return nil
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
