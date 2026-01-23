package updater

import (
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync"
)

var (
	healthMu           sync.Mutex
	initComplete       bool
	relayConnected     bool
	updateCheckSuccess bool
	slotMarkedGood     bool
)

// MarkInitComplete signals that all packages initialized successfully
func MarkInitComplete() {
	healthMu.Lock()
	defer healthMu.Unlock()
	initComplete = true
	log.Println("Updater: Packages initialized successfully")
	tryMarkSlotGood()
}

// MarkRelayConnected signals that relay connection was established
func MarkRelayConnected() {
	healthMu.Lock()
	defer healthMu.Unlock()
	relayConnected = true
	log.Println("Updater: Relay connected successfully")
	tryMarkSlotGood()
}

// markUpdateCheckSuccessful is called after a successful update check.
func markUpdateCheckSuccessful() {
	healthMu.Lock()
	defer healthMu.Unlock()
	updateCheckSuccess = true
	log.Println("Updater: Update check completed successfully")
	tryMarkSlotGood()
}

// tryMarkSlotGood marks the slot as good if all conditions are met.
// Must be called while holding healthMu.
func tryMarkSlotGood() {
	if slotMarkedGood || !initComplete || !relayConnected || !updateCheckSuccess {
		return
	}
	slotMarkedGood = true // Set true even if command execution fails, since executing the command again won't magically work

	if err := confirmSuccessfulBoot(); err != nil {
		log.Printf("Updater: Failed to mark slot as good: %v", err)
	}
}

// confirmSuccessfulBoot marks the current RAUC slot as good
// This calls our custom bootloader backend which updates the good-slot marker
// and clears the trying marker, preventing rollback on next boot
func confirmSuccessfulBoot() error {
	cmd := exec.Command("/usr/lib/rauc/backend/raspberrypi-firmware", "mark-good")
	output, err := cmd.CombinedOutput()
	if err != nil {
		if len(output) > 0 {
			return fmt.Errorf("failed to mark slot as good: %w (output: %s)", err, strings.TrimSpace(string(output)))
		}
		return fmt.Errorf("failed to mark slot as good: %w", err)
	}
	log.Printf("Updater: %s", strings.TrimSpace(string(output)))
	return nil
}
