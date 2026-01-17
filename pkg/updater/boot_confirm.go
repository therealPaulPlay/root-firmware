package updater

import (
	"fmt"
	"log"
	"os/exec"
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
	slotMarkedGood = true

	if err := confirmSuccessfulBoot(); err != nil {
		log.Printf("Updater: Failed to mark slot as good: %v", err)
		slotMarkedGood = false
	}
}

// confirmSuccessfulBoot marks the current RAUC slot as good.
func confirmSuccessfulBoot() error {
	cmd := exec.Command("sudo", "rauc", "status", "mark-good", "booted")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to mark slot as good: %w (output: %s)", err, string(output))
	}
	log.Println("Updater: RAUC slot marked as good")
	return nil
}
