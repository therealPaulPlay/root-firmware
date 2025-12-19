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
	"root-firmware/pkg/globals"
	"root-firmware/pkg/logger"
	"root-firmware/pkg/ml"
	"root-firmware/pkg/pairing"
	"root-firmware/pkg/record"
	"root-firmware/pkg/relaycomm"
	"root-firmware/pkg/speaker"
	"root-firmware/pkg/storage"
	"root-firmware/pkg/updater"
	"root-firmware/pkg/ups"
	"root-firmware/pkg/wifi"
)

//go:embed assets/*
var assets embed.FS

func main() {
	// Initialize logger first to capture all logs
	logger.Init()

	log.Println("Starting")

	// Initialize config
	if err := config.Init(); err != nil {
		log.Fatalf("Failed to initialize config: %v", err)
	}

	// Extract embedded assets to /data partition
	if err := extractAssets(); err != nil {
		log.Fatalf("Failed to extract assets: %v", err)
	}

	// Initialize all packages
	storage.Init()
	devices.Init()
	wifi.Init()
	speaker.Init()
	ups.Init()
	ml.Init()
	record.Init()
	relaycomm.Init()
	updater.Init()

	// Initialize pairing (AP + HTTP server + helper)
	if err := pairing.Init(); err != nil {
		log.Fatalf("Failed to initialize pairing: %v", err)
	}

	// Start relay communication if configured
	if relayURL, ok := config.Get().GetKey("relayUrl"); ok && relayURL != "" {
		relaycomm.RegisterHandlers()
		if err := relaycomm.Get().Start(); err != nil {
			log.Printf("Failed to start relay comm: %v", err)
		}
	}

	// Check for updates every 5 minutes
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()

		updater.Get().CheckForUpdates()
		for range ticker.C {
			updater.Get().CheckForUpdates()
		}
	}()

	// TODO: Play startup sound

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
