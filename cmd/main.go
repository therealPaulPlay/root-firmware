package main

import (
	"embed"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"root-firmware/pkg/config"
	"root-firmware/pkg/devices"
	"root-firmware/pkg/events"
	"root-firmware/pkg/globals"
	"root-firmware/pkg/logger"
	"root-firmware/pkg/metrics"
	"root-firmware/pkg/ml"
	"root-firmware/pkg/notifications"
	"root-firmware/pkg/pairing"
	"root-firmware/pkg/record"
	"root-firmware/pkg/relaycomm"
	"root-firmware/pkg/sfx"
	"root-firmware/pkg/updater"
	"root-firmware/pkg/wifi"
)

//go:embed assets/*
var assets embed.FS

func main() {
	// Initialize logger first to capture all logs
	logger.Init()
	log.Println("Starting")

	// Extract embedded assets to /data partition
	if err := extractAssets(); err != nil {
		log.Fatalf("Failed to extract assets: %v", err)
	}

	// Initialize config and storage, other packages depend on it
	if err := config.Init(); err != nil {
		log.Fatalf("Failed to initialize config: %v", err)
	}
	if err := events.Init(); err != nil {
		log.Fatalf("Failed to initialize events: %v", err)
	}

	// Initialize straightforward packages (not heavy / no errors)
	devices.Init()
	notifications.Init()
	devices.Get().OnRemove(func(deviceID string) {
		if err := notifications.Get().Disable(deviceID); err != nil {
			log.Printf("Failed to disable notifications for removed device %s: %v", deviceID, err)
		}
		if err := relaycomm.Get().ClearClient(deviceID); err != nil {
			log.Printf("Failed to clear client state for removed device %s: %v", deviceID, err)
		}
	})
	metrics.Init()

	// Initialize SFX, recorder, and ML
	if err := sfx.Init(); err != nil {
		log.Printf("Warning: failed to initialize SFX: %v", err)
	}
	if err := record.Init(); err != nil {
		log.Fatalf("Failed to initialize recorder: %v", err)
	}
	if err := ml.Init(); err != nil {
		log.Fatalf("Failed to initialize ML: %v", err)
	}

	// Initialize connectivity
	wifi.Init()
	if err := relaycomm.Init(); err != nil {
		log.Fatalf("Failed to initialize relaycomm: %v", err)
	}
	record.Get().OnMicChanged = relaycomm.SyncAudioStreams
	updater.Init()

	// Initialize pairing (BLE + helper)
	if err := pairing.Init(); err != nil {
		log.Fatalf("Failed to initialize pairing: %v", err)
	}

	// Mark initialization complete for boot confirmation tracking
	updater.MarkInitComplete()

	// Start relay connection (if relay domain configured)
	relaycomm.Get().Start()

	// Check for updates after 5s, then every 15 minutes
	go func() {
		time.Sleep(5 * time.Second)
		updater.Get().CheckForUpdates()
		for range time.Tick(15 * time.Minute) {
			updater.Get().CheckForUpdates()
		}
	}()

	// Play startup sound
	sfx.Get().PlayStartup()

	// Wait for interrupt signal, keep everything alive until then
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan
}

func extractAssets() error {
	entries, err := assets.ReadDir("assets")
	if err != nil {
		return fmt.Errorf("failed to read assets: %w", err)
	}

	if err := os.MkdirAll(globals.AssetsPath, 0755); err != nil {
		return fmt.Errorf("failed to create assets dir: %w", err)
	}

	return extractDir("assets", globals.AssetsPath, entries)
}

func extractDir(embedPath, destPath string, entries []os.DirEntry) error {
	for _, entry := range entries {
		embedFile := filepath.Join(embedPath, entry.Name())
		destFile := filepath.Join(destPath, entry.Name())

		if entry.IsDir() {
			if err := os.MkdirAll(destFile, 0755); err != nil {
				return err
			}
			subEntries, err := assets.ReadDir(embedFile)
			if err != nil {
				return err
			}
			if err := extractDir(embedFile, destFile, subEntries); err != nil {
				return err
			}
		} else {
			data, err := assets.ReadFile(embedFile)
			if err != nil {
				return err
			}
			if err := os.WriteFile(destFile, data, 0644); err != nil {
				return err
			}
		}
	}

	return nil
}
