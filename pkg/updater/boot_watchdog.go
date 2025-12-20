package updater

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"

	"root-firmware/pkg/globals"
)

// Confirms successful boot and removes boot counter
func ConfirmSuccessfulBoot() error {
	if err := os.Remove(globals.BootCountPath); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("failed to remove boot counter: %w", err)
		}
	}
	log.Println("Boot confirmed successfully")
	return nil
}

// InstallBootWatchdog installs the systemd service for automatic rollback
// This creates a boot-watchdog service that runs before the firmware starts
// and automatically rolls back to the previous partition if boot fails
func InstallBootWatchdog() error {
	// Create the boot watchdog script
	script := `#!/bin/bash
BOOTCOUNT_FILE="` + globals.BootCountPath + `"
CMDLINE_FILE="` + globals.BootCmdlinePath + `"
PARTITION_A="` + globals.PartitionA + `"
PARTITION_B="` + globals.PartitionB + `"

if [ ! -f "$BOOTCOUNT_FILE" ]; then
    # No boot counter, stable boot
    exit 0
fi

COUNT=$(cat "$BOOTCOUNT_FILE")

# Decrement counter first
NEW_COUNT=$((COUNT - 1))
echo "$NEW_COUNT" > "$BOOTCOUNT_FILE"
echo "Boot attempt $((` + fmt.Sprintf("%d", globals.MaxBootAttempts) + ` - NEW_COUNT))/` + fmt.Sprintf("%d", globals.MaxBootAttempts) + `"

# Check if attempts exhausted
if [ "$NEW_COUNT" -le 0 ]; then
    # Boot attempts exhausted, rollback
    echo "Boot counter exhausted, rolling back partition"

    # Get current root partition
    CURRENT_ROOT=$(grep -o 'root=/dev/[^ ]*' /proc/cmdline | cut -d'=' -f2)

    # Switch to other partition
    if [ "$CURRENT_ROOT" = "$PARTITION_A" ]; then
        NEW_ROOT="$PARTITION_B"
    else
        NEW_ROOT="$PARTITION_A"
    fi

    # Update cmdline.txt
    sed -i "s|root=/dev/\S*|root=$NEW_ROOT|" "$CMDLINE_FILE"

    # Remove boot counter and reboot
    rm -f "$BOOTCOUNT_FILE"
    echo "Rolling back to $NEW_ROOT and rebooting..."
    reboot
    exit 0
fi
`

	if err := os.WriteFile("/usr/local/bin/boot-watchdog.sh", []byte(script), 0755); err != nil {
		return fmt.Errorf("failed to write boot watchdog script: %w", err)
	}

	// Create systemd service
	service := `[Unit]
Description=Boot Watchdog - Automatic Firmware Rollback
DefaultDependencies=no
Before=basic.target

[Service]
Type=oneshot
ExecStart=/usr/local/bin/boot-watchdog.sh
RemainAfterExit=yes

[Install]
WantedBy=sysinit.target
`

	if err := os.WriteFile("/etc/systemd/system/boot-watchdog.service", []byte(service), 0644); err != nil {
		return fmt.Errorf("failed to write systemd service: %w", err)
	}

	// Enable the service
	if err := exec.Command("systemctl", "daemon-reload").Run(); err != nil {
		return fmt.Errorf("failed to reload systemd: %w", err)
	}

	if err := exec.Command("systemctl", "enable", "boot-watchdog.service").Run(); err != nil {
		return fmt.Errorf("failed to enable boot watchdog: %w", err)
	}

	log.Println("Boot watchdog installed and enabled")
	return nil
}

// Reads /proc/cmdline to determine which partition is currently booted
func getActivePartition() (string, error) {
	data, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return "", fmt.Errorf("failed to read /proc/cmdline: %w", err)
	}

	matches := regexp.MustCompile(`root=(/dev/\S+)`).FindStringSubmatch(string(data))
	if len(matches) < 2 {
		return "", fmt.Errorf("could not detect root partition in cmdline")
	}

	partition := matches[1]

	// Validate it's one of our configured partitions
	if partition != globals.PartitionA && partition != globals.PartitionB {
		return "", fmt.Errorf("active partition %s is not one of configured partitions (%s, %s)",
			partition, globals.PartitionA, globals.PartitionB)
	}

	return partition, nil
}

// Returns the partition that is NOT currently active
func getInactivePartition(active string) string {
	if active == globals.PartitionA {
		return globals.PartitionB
	}
	return globals.PartitionA
}

// Updates cmdline.txt to boot from a different partition
func switchBootPartition(newRoot string) error {
	data, err := os.ReadFile(globals.BootCmdlinePath)
	if err != nil {
		return fmt.Errorf("failed to read boot config: %w", err)
	}

	newCmdline := regexp.MustCompile(`root=/dev/\S+`).ReplaceAllString(string(data), "root="+newRoot)

	if err := os.WriteFile(globals.BootCmdlinePath, []byte(newCmdline), 0644); err != nil {
		return fmt.Errorf("failed to write boot config: %w", err)
	}

	log.Printf("Boot partition switched to %s", newRoot)
	return nil
}

// Ssets the boot attempt counter for automatic rollback
func setBootCounter() error {
	counter := fmt.Sprintf("%d\n", globals.MaxBootAttempts)
	if err := os.WriteFile(globals.BootCountPath, []byte(counter), 0644); err != nil {
		return fmt.Errorf("failed to write boot counter: %w", err)
	}
	log.Printf("Boot counter set to %d attempts", globals.MaxBootAttempts)
	return nil
}
