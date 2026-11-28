package updater

import (
	"fmt"
	"log"
	"os"
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
	if initComplete {
		return
	}
	initComplete = true
	log.Println("Updater: Packages initialized successfully")
	tryMarkSlotGood()
}

// MarkRelayConnected signals that relay connection was established
// Returns true on the first connection since boot
func MarkRelayConnected() bool {
	healthMu.Lock()
	defer healthMu.Unlock()
	if relayConnected {
		return false
	}
	relayConnected = true
	log.Println("Updater: Relay connected successfully")
	tryMarkSlotGood()
	return true
}

// markUpdateCheckSuccessful is called after a successful update check
func markUpdateCheckSuccessful() {
	healthMu.Lock()
	defer healthMu.Unlock()
	if updateCheckSuccess {
		return
	}
	updateCheckSuccess = true
	log.Println("Updater: Update check completed successfully")
	tryMarkSlotGood()
}

// tryMarkSlotGood marks the slot as good if all conditions are met
// Must be called while holding healthMu
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
// Calls our custom bootloader backend to make the booted slot permanent (no rollback on next boot)
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

// GetCurrentSlot returns the current boot slot ("A" or "B") by reading what partition /proc/cmdline points to
func GetCurrentSlot() string {
	data, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return "unknown"
	}
	for field := range strings.FieldsSeq(string(data)) {
		if strings.HasPrefix(field, "root=") {
			// Handles both root=/dev/mmcblk0p5 and root=PARTUUID=...-05
			switch field[len(field)-1] {
			case '5':
				return "A"
			case '6':
				return "B"
			}
		}
	}
	return "unknown"
}
